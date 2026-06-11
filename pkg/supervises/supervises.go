package supervises

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mattn/go-shellwords"
	"go.iscode.ca/supervises/pkg/broadcast"
	"golang.org/x/sync/errgroup"
)

var (
	ErrNoCommand = errors.New("no command provided")
)

type Config struct {
	onStart func(*Cmd, int)
	onExit  func(*Cmd, *ExitError) *ExitError
	stdin   io.ReadCloser
}

type Option func(*Config)

type Supervisor struct {
	ctx  context.Context
	cfg  *Config
	cmds []*Cmd

	bsig   broadcast.Broadcaster[os.Signal]
	bstdin broadcast.Broadcaster[[]byte]
	eof    atomic.Bool
}

// WithOnStart sets a callback function executed every time a command
// successfully starts, providing the original configuration and the active PID.
func WithOnStart(f func(cmd *Cmd, pid int)) Option {
	return func(c *Config) {
		if f != nil {
			c.onStart = f
		}
	}
}

// WithOnExit sets the function called when the supervised process
// exits. The supervised process is restarted if the function returns nil.
func WithOnExit(onExit func(*Cmd, *ExitError) *ExitError) Option {
	return func(o *Config) {
		if onExit != nil {
			o.onExit = onExit
		}
	}
}

// WithStdin sets the source for standard input.
func WithStdin(r io.ReadCloser) Option {
	return func(o *Config) {
		o.stdin = r
	}
}

// New returns configuration for supervisors.
func New(ctx context.Context, cmds []*Cmd, opts ...Option) *Supervisor {
	cfg := &Config{
		onStart: func(_ *Cmd, _ int) {},
		onExit: func(_ *Cmd, eerr *ExitError) *ExitError {
			if eerr != nil {
				return eerr
			}

			// Restart the process.
			time.Sleep(time.Second)
			return nil
		},
		stdin: os.Stdin,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return &Supervisor{
		ctx:  ctx,
		cfg:  cfg,
		cmds: cmds,

		bsig:   broadcast.NewBroadcaster[os.Signal](len(cmds)),
		bstdin: broadcast.NewBroadcaster[[]byte](len(cmds)),
	}
}

type Cmd struct {
	exec.Cmd
	EOF    bool
	Cancel func(*exec.Cmd) error
}

func (c *Cmd) String() string {
	b := new(strings.Builder)
	b.WriteString(c.Path)
	for _, a := range c.Args[1:] {
		b.WriteByte(' ')
		b.WriteString(a)
	}
	return b.String()
}

// Parse accepts a list of commands to be supervised and returns an error
// if the executable is not found or the commands is not a valid shell
// expression.
//
// # Sigils
//
// Commands may be prefixed by sigils which modify the command behaviour:
//
// - @: run in shell
//
//	supervises @'nc -l 8080 >nc.log'
//
// - =: discard stdin, stdout and stderr
//
//	# equivalent to: supervises @'nc -l 8080 </dev/null >/dev/null 2>&1'
//	supervises ='nc -l 8080'
//
// * =0: discard stdin
//
//	# equivalent to: supervises @'nc -l 8080 </dev/null'
//	supervises =0'nc -l 8080'
//
// * =1: discard stdout
//
//	# equivalent to: supervises @'nc -l 8080 >/dev/null'
//	supervises =1'nc -l 8080'
//
// * =2: discard stderr
//
//	# equivalent to: supervises @'nc -l 8080 2>/dev/null'
//	supervises =2'nc -l 8080'
//
// * =3: discard stdout and stderr
//
//	# equivalent to: supervises @'nc -l 8080 </dev/null >/dev/null 2>&1'
//	supervises =3'nc -l 8080'
func Parse(args ...string) ([]*Cmd, error) {
	cmds := make([]*Cmd, 0, len(args))
	for _, v := range args {
		cmd, err := cmd(v)
		if err != nil {
			return cmds, err
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

func cmd(arg string) (*Cmd, error) {
	c := &Cmd{
		Cmd: exec.Cmd{
			Env:    os.Environ(),
			Stdout: os.Stdout,
			Stderr: os.Stderr,
			SysProcAttr: &syscall.SysProcAttr{
				Pdeathsig: syscall.SIGKILL,
			},
		},
		Cancel: func(cmd *exec.Cmd) error {
			return cmd.Process.Signal(syscall.SIGKILL)
		},
	}

	switch {
	case strings.HasPrefix(arg, "@"):
		c.Path = "/bin/sh"
		c.Args = []string{c.Path, "-c", strings.TrimPrefix(arg, "@")}
		return c, nil
	case strings.HasPrefix(arg, "=0"):
		c.EOF = true
		arg = strings.TrimPrefix(arg, "=0")
	case strings.HasPrefix(arg, "=1"):
		c.Stdout = io.Discard
		arg = strings.TrimPrefix(arg, "=1")
	case strings.HasPrefix(arg, "=2"):
		c.Stderr = io.Discard
		arg = strings.TrimPrefix(arg, "=2")
	case strings.HasPrefix(arg, "=3"):
		c.Stdout = io.Discard
		c.Stderr = io.Discard
		arg = strings.TrimPrefix(arg, "=3")
	case strings.HasPrefix(arg, "="):
		c.Stdout = io.Discard
		c.Stderr = io.Discard
		c.EOF = true
		arg = strings.TrimPrefix(arg, "=")
	}

	argv, err := shellwords.Parse(arg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", arg, err)
	}

	if len(argv) == 0 {
		return nil, fmt.Errorf("%s: %w", arg, ErrNoCommand)
	}

	arg0, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", arg, err)
	}

	c.Path = arg0
	c.Args = argv

	return c, nil
}

func (sv *Supervisor) stdinhandler(ctx context.Context) error {
	defer func() {
		_ = sv.cfg.stdin.Close()
		sv.eof.Store(true)
		sv.bstdin.Submit(nil)
	}()

	buf := make([]byte, 4096)
	ch := make(chan []byte, 1)

	go func() {
		defer close(ch)
		for {
			n, err := sv.cfg.stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case ch <- chunk:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}

		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case chunk, ok := <-ch:
			if !ok {
				return nil
			}
			sv.bstdin.Submit(chunk)
		}
	}
}

// ForwardSignals is a convenience function that intercepts the specified OS signals
// and broadcasts them to the provided Supervisor. It runs in the background until
// the context is canceled.
func ForwardSignals(ctx context.Context, sv *Supervisor, sigs ...os.Signal) {
	if len(sigs) == 0 {
		return
	}

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, sigs...)

	go func() {
		defer signal.Stop(sigch)
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-sigch:
				sv.SignalAll(sig)
			}
		}
	}()
}

// DefaultSignals represents the standard set of OS signals commonly
// forwarded to supervised processes. Note that forwarding a signal
// does not guarantee process termination; child processes may trap
// or ignore these signals.
var DefaultSignals = []os.Signal{
	syscall.SIGHUP,
	syscall.SIGINT,
	syscall.SIGQUIT,
	syscall.SIGALRM,
	syscall.SIGTERM,
	syscall.SIGUSR1,
	syscall.SIGUSR2,
}

// Run runs, monitors and onExits a list of commands.
func (sv *Supervisor) Run() error {
	defer func() {
		_ = sv.bsig.Close()
		_ = sv.bstdin.Close()
	}()

	g, ctx := errgroup.WithContext(sv.ctx)

	var startupWg sync.WaitGroup
	startupWg.Add(len(sv.cmds))

	g.Go(func() error {
		startupWg.Wait()
		return sv.stdinhandler(ctx)
	})

	for _, v := range sv.cmds {
		g.Go(func() error {
			isFirstRun := true

			for {
				var notifyReady func()
				if isFirstRun {
					var once sync.Once
					notifyReady = func() { once.Do(func() { startupWg.Done() }) }
				} else {
					notifyReady = func() {} // No-op for subsequent onExits
				}

				err := sv.run(ctx, v, notifyReady)
				isFirstRun = false

				select {
				case <-ctx.Done():
					return err
				default:
				}

				if err != nil && errors.Is(err.Err, context.Canceled) {
					return err
				}

				if rerr := sv.cfg.onExit(v, err); rerr != nil {
					return rerr
				}
			}
		})
	}

	return g.Wait()
}

// SignalAll broadcasts the given OS signal to all currently supervised processes.
func (sv *Supervisor) SignalAll(sig os.Signal) {
	sv.bsig.Submit(sig)
}

type ExitError struct {
	Err      error
	Cmd      *exec.Cmd
	ExitCode int
}

func (e *ExitError) Error() string {
	if e == nil {
		return "Exited successfully"
	}
	if e.Err == nil {
		return fmt.Sprintf("Exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("Exited with status %d: %s", e.ExitCode, e.Err.Error())
}

func (e *ExitError) String() string {
	// Fixed by https://github.com/golang/go/commit/33241d7298e0c621cfc4cc9f878dba9eff2b1c3d
	if len(e.Cmd.Args) == 0 {
		e.Cmd.Args = []string{e.Cmd.Path}
	}
	return e.Cmd.String()
}

func (sv *Supervisor) run(ctx context.Context, argv *Cmd, notifyReady func()) *ExitError {
	cmd := exec.CommandContext(ctx, argv.Path, argv.Args[1:]...)
	cmd.Stdout = argv.Stdout
	cmd.Stderr = argv.Stderr
	cmd.Env = argv.Env
	cmd.Cancel = func() error {
		return argv.Cancel(cmd)
	}
	cmd.Dir = argv.Dir
	cmd.SysProcAttr = argv.SysProcAttr
	cmd.WaitDelay = argv.WaitDelay

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		notifyReady()
		return &ExitError{Cmd: cmd, Err: err, ExitCode: 126}
	}

	if err := cmd.Start(); err != nil {
		notifyReady()
		status := cmd.ProcessState.ExitCode()
		if status < 0 {
			status = 126
		}
		return &ExitError{
			Cmd:      cmd,
			Err:      err,
			ExitCode: status,
		}
	}

	sv.cfg.onStart(argv, cmd.Process.Pid)

	if sv.eof.Load() || argv.EOF {
		_ = stdinPipe.Close()
		notifyReady()
	} else {
		byteCh := make(chan []byte, 1)
		sv.bstdin.Register(byteCh)
		defer sv.bstdin.Unregister(byteCh)

		runDone := make(chan struct{})
		defer close(runDone)

		notifyReady()

		go func() {
			defer func() {
				_ = stdinPipe.Close()
			}()
			for {
				select {
				case <-runDone:
					return
				case chunk, ok := <-byteCh:
					if !ok || len(chunk) == 0 {
						return
					}
					_, err := stdinPipe.Write(chunk)
					if err != nil {
						return
					}
				}
			}
		}()
	}

	waitch := make(chan error, 1)
	go func() {
		waitch <- cmd.Wait()
	}()

	return sv.waitpid(waitch, cmd)
}

func (sv *Supervisor) waitpid(waitch <-chan error, cmd *exec.Cmd) *ExitError {
	var ee *exec.ExitError

	ch := make(chan os.Signal, 1)
	sv.bsig.Register(ch)
	defer sv.bsig.Unregister(ch)

	for {
		select {
		case sig := <-ch:
			_ = cmd.Process.Signal(sig)
		case err := <-waitch:
			if err == nil {
				return nil
			}

			if !errors.As(err, &ee) {
				return &ExitError{
					Cmd:      cmd,
					ExitCode: 128,
					Err:      err,
				}
			}

			waitStatus, ok := ee.Sys().(syscall.WaitStatus)
			if !ok {
				return &ExitError{
					Cmd:      cmd,
					ExitCode: 128,
					Err:      err,
				}
			}

			if waitStatus.Signaled() {
				return &ExitError{
					Cmd:      cmd,
					ExitCode: 128 + int(waitStatus.Signal()),
					Err:      err,
				}
			}

			return &ExitError{
				Cmd:      cmd,
				ExitCode: waitStatus.ExitStatus(),
				Err:      err,
			}
		}
	}
}
