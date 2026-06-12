package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"go.iscode.ca/supervises/pkg/supervises"
)

const (
	version = "0.7.2"
)

var (
	programLevel = new(slog.LevelVar)
)

func usage() {
	fmt.Fprintf(os.Stderr, `%s v%s
Usage: %s "<cmd> <args>" [...]

Command line supervisor.

Sigils:

	@: run in shell

		supervises @'nc -l 8080 >nc.log'

	=: discard stdout/stderr

		# equivalent to: supervises @'nc -l 8080 >/dev/null 2>&1'
		supervises ='nc -l 8080'

	=1: discard stdout

		# equivalent to: supervises @'nc -l 8080 >/dev/null'
		supervises =1'nc -l 8080'

	=2: discard stderr

		# equivalent to: supervises @'nc -l 8080 2>/dev/null'
		supervises =2'nc -l 8080'

Options:
`, path.Base(os.Args[0]), version, os.Args[0])
	flag.PrintDefaults()
}

func main() {
	l := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: programLevel}).
		WithAttrs([]slog.Attr{slog.String("arg0", path.Base(os.Args[0]))}))
	slog.SetDefault(l)

	help := flag.Bool("help", false, "Display usage")
	sig := flag.Int("signal", int(syscall.SIGKILL), "signal sent to subprocesses on exit")
	verbose := flag.Bool("verbose", false, "Enable debug messages")

	restartWait := flag.Duration("restart-wait", 1*time.Second, "restart backoff interval")
	restartCount := flag.Int("restart-count", 0, "restart limit before exiting (0: no limit)")
	restartPeriod := flag.Duration("restart-period", 0, "time interval for restarts (0: no limit)")
	errExit := flag.Bool("errexit", false, "restarts apply to tasks exiting with a non-0 status")
	strategy := flag.String("strategy", "always", "restart strategy (always, on-error, on-success)")

	flag.Usage = func() { usage() }
	flag.Parse()

	if *verbose {
		programLevel.Set(slog.LevelDebug)
	}

	if *help || flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	switch *strategy {
	case "always", "on-error", "on-success":
	default:
		fmt.Fprintf(os.Stderr, "invalid strategy: %s (must be one of: always, on-error, on-success)\n", *strategy)
		usage()
		os.Exit(2)
	}

	cmds, err := supervises.Parse(flag.Args()...)
	if err != nil {
		l.Info("parse error", "error", err)
		os.Exit(2)
	}

	type commandState struct {
		count  atomic.Int32
		ticker *time.Ticker
	}

	cancelFunc := func(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.Signal(*sig)) }

	states := make(map[*supervises.Cmd]*commandState)
	for _, cmd := range cmds {
		cmd.Cancel = cancelFunc

		var t *time.Ticker
		if *restartPeriod > 0 {
			t = time.NewTicker(*restartPeriod)
		}
		cs := &commandState{
			ticker: t,
		}
		cs.count.Store(int32(*restartCount))
		states[cmd] = cs
	}

	restart := func(c *supervises.Cmd, ee *supervises.ExitError) *supervises.ExitError {
		cs := states[c]

		if ee != nil {
			l.Debug("command exited", "argv", ee.String(), "status", ee.ExitCode, "error", ee.Err)
		}

		switch *strategy {
		case "on-error":
			if ee == nil {
				return &supervises.ExitError{
					Cmd: &exec.Cmd{
						Path: c.String(),
					},
				}
			}
		case "on-success":
			if ee != nil {
				return ee
			}
		}

		if *restartCount > 0 {
			if cs.ticker != nil {
				select {
				case <-cs.ticker.C:
					cs.count.Store(int32(*restartCount))
				default:
				}
			}

			if *errExit {
				if ee != nil {
					cs.count.Add(-1)
				}
			} else {
				cs.count.Add(-1)
			}

			if cs.count.Load() <= 0 {
				if ee != nil {
					return ee
				}
				return &supervises.ExitError{
					Cmd: &exec.Cmd{
						Path: c.String(),
					},
				}
			}
		}

		time.Sleep(*restartWait)
		return nil
	}

	var ee *supervises.ExitError

	ctx, cancel := context.WithCancel(context.Background())

	sv := supervises.New(ctx, cmds,
		supervises.WithOnExit(restart),
	)

	supervises.ForwardSignals(ctx, sv, slices.DeleteFunc(supervises.DefaultSignals, func(sig os.Signal) bool { return sig == syscall.SIGINT })...)

	sigintCh := make(chan os.Signal, 1)
	signal.Notify(sigintCh, syscall.SIGINT)

	go func() {
		count := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigintCh:
				if count > 0 {
					cancel()
					return
				}

				sv.SignalAll(syscall.SIGINT)
				count++
			}
		}
	}()

	if err := sv.Run(); err != nil {
		if !errors.As(err, &ee) {
			l.Debug("command exited", "error", err)
			os.Exit(128)
		}

		l.Debug("command exited", "argv", ee.String(), "status", ee.ExitCode, "error", ee.Err)
		os.Exit(ee.ExitCode)
	}
}
