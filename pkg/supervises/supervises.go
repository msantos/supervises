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
	ErrInvalidCommand = errors.New("invalid command")
)

type Config struct {
	cancelFunc func(*exec.Cmd) error
	retry      func(*Cmd, *ExitError) *ExitError
	stdin      io.ReadCloser
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

// WithCancelFunc sets the function to reap cancelled subprocesses.
func WithCancelFunc(f func(*exec.Cmd) error) Option {
	return func(o *Config) {
		o.cancelFunc = f
	}
}

// WithRetry sets the retry behaviour.
func WithRetry(retry func(*Cmd, *ExitError) *ExitError) Option {
	return func(o *Config) {
		if retry != nil {
			o.retry = retry
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
//
// # Cancel Function
//
// The default cancel function signals supervised processes if the supervisor
// exits, e.g., due to timeout. The cancel signal defaults to SIGKILL.
func New(ctx context.Context, cmds []*Cmd, opts ...Option) *Supervisor {
	cfg := &Config{
		cancelFunc: func(cmd *exec.Cmd) error {
			return cmd.Process.Signal(syscall.SIGKILL)
		},

		retry: func(_ *Cmd, _ *ExitError) *ExitError {
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
	Path        string
	Args        []string
	Env         []string
	Dir         string
	Stdout      io.Writer
	Stderr      io.Writer
	ExtraFiles  []*os.File
	SysProcAttr *syscall.SysProcAttr
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
// - =: discard stdout/stderr
//
//	# equivalent to: supervises @'nc -l 8080 >/dev/null 2>&1'
//	supervises ='nc -l 8080'
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
		Env:    os.Environ(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{
			Pdeathsig: syscall.SIGKILL,
		},
	}

	switch {
	case strings.HasPrefix(arg, "@"):
		c.Path = "/bin/sh"
		c.Args = []string{c.Path, "-c", strings.TrimPrefix(arg, "@")}
		return c, nil
	case strings.HasPrefix(arg, "=1"):
		c.Stdout = io.Discard
		arg = strings.TrimPrefix(arg, "=1")
	case strings.HasPrefix(arg, "=2"):
		c.Stderr = io.Discard
		arg = strings.TrimPrefix(arg, "=2")
	case strings.HasPrefix(arg, "="):
		c.Stdout = io.Discard
		c.Stderr = io.Discard
		arg = strings.TrimPrefix(arg, "=")
	}

	argv, err := shellwords.Parse(arg)
	if err != nil {
		return nil, err
	}

	if len(argv) == 0 {
		return nil, ErrInvalidCommand
	}

	arg0, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
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

// Run runs, monitors and restarts a list of commands.
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
					notifyReady = func() {} // No-op for subsequent restarts
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

				if rerr := sv.cfg.retry(v, err); rerr != nil {
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
	return e.Cmd.String()
}

func (sv *Supervisor) run(ctx context.Context, argv *Cmd, notifyReady func()) *ExitError {
	cmd := exec.CommandContext(ctx, argv.Path, argv.Args[1:]...)
	cmd.Stdout = argv.Stdout
	cmd.Stderr = argv.Stderr
	cmd.Env = argv.Env
	cmd.Cancel = func() error {
		return sv.cfg.cancelFunc(cmd)
	}
	cmd.SysProcAttr = argv.SysProcAttr

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

	if sv.eof.Load() {
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
