package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.iscode.ca/supervises/pkg/supervises"
)

type commandState struct {
	mu     sync.Mutex
	count  atomic.Int32
	ticker *time.Ticker
	pid    int
	killed bool
}

type StrategyManager struct {
	ctx           context.Context
	strategy      string
	cmds          []*supervises.Cmd
	states        map[*supervises.Cmd]*commandState
	restartCount  int
	restartPeriod time.Duration
	restartWait   time.Duration
	errExit       bool
	signal        syscall.Signal
	logger        *slog.Logger

	muLock      sync.Mutex
	firstFailed *supervises.ExitError
}

func (m *StrategyManager) saveFailedError(e *supervises.ExitError) {
	if e == nil {
		return
	}
	m.muLock.Lock()
	defer m.muLock.Unlock()
	if m.firstFailed == nil {
		m.firstFailed = e
	}
}

func (m *StrategyManager) FirstFailedError() *supervises.ExitError {
	m.muLock.Lock()
	defer m.muLock.Unlock()
	return m.firstFailed
}

func (m *StrategyManager) OnStart(c *supervises.Cmd, pid int) {
	cs := m.states[c]
	cs.mu.Lock()
	cs.pid = pid
	cs.mu.Unlock()
}

func (m *StrategyManager) OnExit(c *supervises.Cmd, e *supervises.ExitError) *supervises.ExitError {
	cs := m.states[c]

	cs.mu.Lock()
	wasKilled := cs.killed
	cs.killed = false
	cs.pid = 0
	cs.mu.Unlock()

	if e != nil {
		m.logger.Debug("command exited", "argv", e.String(), "status", e.ExitCode, "error", e.Err)
	}

	// Handle Erlang supervisor strategies and other restart policies
	switch m.strategy {
	case "one-for-all", "one_for_all":
		if e == nil {
			// Success: transient process behavior (don't restart, keep other processes running)
			return &supervises.ExitError{
				Err: supervises.ErrStop,
			}
		}

		if !wasKilled {
			// This was a real crash, terminate all other processes
			m.terminateAllOthers(c)
		}

	case "one-for-all-always", "one_for_all_always":
		if !wasKilled {
			// This was a normal exit or crash, terminate all other processes
			m.terminateAllOthers(c)
		}

	case "one-for-all-once", "one_for_all_once":
		if !wasKilled {
			// Terminate all other processes
			m.terminateAllOthers(c)
		}
		if e == nil {
			return &supervises.ExitError{
				Cmd: &exec.Cmd{
					Path: c.String(),
				},
				ExitCode: 0,
			}
		}
		return e

	case "rest-for-one", "rest_for_one":
		if e == nil {
			// Success: transient process behavior (don't restart, keep other processes running)
			return &supervises.ExitError{
				Err: supervises.ErrStop,
			}
		}

		if !wasKilled {
			// This was a real crash, terminate all processes started after this one
			m.terminateAllAfter(c)
		}

	case "rest-for-one-always", "rest_for_one_always":
		if !wasKilled {
			// This was a normal exit or crash, terminate all processes started after this one
			m.terminateAllAfter(c)
		}

	case "on-error":
		if e == nil {
			// Success: don't restart, but let other processes run
			return &supervises.ExitError{
				Err: supervises.ErrStop,
			}
		}

	case "on-success":
		if e != nil {
			// Failure: don't restart, but let other processes run
			m.saveFailedError(e)
			return &supervises.ExitError{
				Err: supervises.ErrStop,
			}
		}
	}

	// Apply restart limits
	if m.restartCount > 0 {
		if cs.ticker != nil {
			select {
			case <-cs.ticker.C:
				cs.count.Store(int32(m.restartCount))
			default:
			}
		}

		if m.errExit {
			if e != nil {
				cs.count.Add(-1)
			}
		} else {
			cs.count.Add(-1)
		}

		if cs.count.Load() <= 0 {
			if e != nil {
				return e
			}
			return &supervises.ExitError{
				Cmd: &exec.Cmd{
					Path: c.String(),
				},
			}
		}
	}

	if m.ctx == nil {
		time.Sleep(m.restartWait)
		return nil
	}

	t := time.NewTimer(m.restartWait)
	defer t.Stop()

	select {
	case <-m.ctx.Done():
		return &supervises.ExitError{
			Err: m.ctx.Err(),
		}
	case <-t.C:
	}

	return nil
}

func (m *StrategyManager) terminateAllOthers(c *supervises.Cmd) {
	for _, cmd := range m.cmds {
		if cmd == c {
			continue
		}
		m.terminate(cmd)
	}
}

func (m *StrategyManager) terminateAllAfter(c *supervises.Cmd) {
	found := false
	for _, cmd := range m.cmds {
		if cmd == c {
			found = true
			continue
		}
		if found {
			m.terminate(cmd)
		}
	}
}

func (m *StrategyManager) terminate(c *supervises.Cmd) {
	cs := m.states[c]
	cs.mu.Lock()
	pid := cs.pid
	if pid > 0 {
		cs.killed = true
	}
	cs.mu.Unlock()

	if pid > 0 {
		m.logger.Debug("terminating process for strategy", "path", c.Path, "pid", pid)
		p, err := os.FindProcess(pid)
		if err == nil {
			_ = p.Signal(m.signal)
		}
	}
}
