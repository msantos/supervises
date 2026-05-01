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
	"syscall"
	"time"

	"github.com/dustin/go-broadcast"
	"github.com/mattn/go-shellwords"
	"golang.org/x/sync/errgroup"
)

var (
	ErrInvalidCommand = errors.New("invalid command")
	ErrSignal         = errors.New("terminated by signal")
)

type Opt struct {
	uctx         context.Context
	ctx          context.Context
	cancel       context.CancelFunc
	g            *errgroup.Group
	cancelFunc   func(*exec.Cmd, syscall.Signal) error
	cancelSignal syscall.Signal
	signals      []os.Signal
	retry        func(*Cmd, *ExitError) *ExitError
}

type Option func(*Opt)

// WithCancelFunc sets the function to reap cancelled subprocesses.
func WithCancelFunc(f func(*exec.Cmd, syscall.Signal) error) Option {
	return func(o *Opt) {
		o.cancelFunc = f
	}
}

// WithCancelSignal sets the signal sent to subprocesses on exit.
func WithCancelSignal(sig syscall.Signal) Option {
	return func(o *Opt) {
		o.cancelSignal = sig
	}
}

// WithNotifySignals sets trapped signals by the supervisor.
func WithNotifySignals(sigs ...os.Signal) Option {
	return func(o *Opt) {
		o.signals = sigs
	}
}

// WithRetry sets the retry behaviour.
func WithRetry(retry func(*Cmd, *ExitError) *ExitError) Option {
	return func(o *Opt) {
		if retry != nil {
			o.retry = retry
		}
	}
}

// New returns configuration for supervisors.
//
// # Signals
//
// By default, the following signals are intercepted and forwarded to
// supervised processes:
//
// - SIGHUP
// - SIGINT
// - SIGQUIT
// - SIGALRM
// - SIGTERM
// - SIGUSR1
// - SIGUSR2
//
// If SIGINT is caught, the first signal will be broadcasted to supervised
// processes. A second SIGINT will cause the supervisor to exit, sending
// the cancel signal (default: SIGKILL).
//
// # Cancel Function and Signal
//
// The default cancel function signals supervised processes if the supervisor
// exits, e.g., due to timeout. The cancel signal defaults to SIGKILL.
func New(ctx context.Context, opt ...Option) *Opt {
	o := &Opt{
		signals: []os.Signal{
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGQUIT,
			syscall.SIGALRM,
			syscall.SIGTERM,
			syscall.SIGUSR1,
			syscall.SIGUSR2,
		},
		cancelFunc: func(cmd *exec.Cmd, sig syscall.Signal) error {
			return cmd.Process.Signal(sig)
		},
		cancelSignal: syscall.SIGKILL,

		retry: func(_ *Cmd, _ *ExitError) *ExitError {
			time.Sleep(time.Second)
			return nil
		},
	}

	for _, fn := range opt {
		fn(o)
	}

	o.uctx = ctx
	o.Reset()

	return o
}

func (o *Opt) Reset() {
	if o.cancel != nil {
		o.cancel()
	}

	cCtx, cancel := context.WithCancel(o.uctx)
	g, ctx := errgroup.WithContext(cCtx)

	o.g = g
	o.ctx = ctx
	o.cancel = cancel
}

func (o *Opt) Context() context.Context {
	return o.ctx
}

type Cmd struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	Stdin       io.Reader
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

// Cmd accepts a list of commands to be supervised and returns an error
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
func (o *Opt) Cmd(args ...string) ([]*Cmd, error) {
	cmds := make([]*Cmd, 0, len(args))
	for _, v := range args {
		cmd, err := o.cmd(v)
		if err != nil {
			return cmds, err
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

func (o *Opt) cmd(arg string) (*Cmd, error) {
	c := &Cmd{
		Env:    os.Environ(),
		Stdin:  os.Stdin,
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
		return nil, &ExitError{
			Cmd: &exec.Cmd{
				Path: os.Args[0],
				Args: []string{arg},
			},
			Err:      err,
			ExitCode: 2,
		}
	}

	if len(argv) == 0 {
		return nil, &ExitError{
			Cmd: &exec.Cmd{
				Path: os.Args[0],
				Args: []string{arg},
			},
			Err:      ErrInvalidCommand,
			ExitCode: 2,
		}
	}

	arg0, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, &ExitError{
			Cmd: &exec.Cmd{
				Path: argv[0],
				Args: argv,
			},
			Err:      err,
			ExitCode: 127,
		}
	}

	c.Path = arg0
	c.Args = argv

	return c, nil
}

func (o *Opt) sighandler(ctx context.Context, b broadcast.Broadcaster) error {
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, o.signals...)
	defer signal.Stop(sigch)

	count := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case v := <-sigch:
			if v == syscall.SIGINT {
				if count > 0 {
					return &ExitError{
						Cmd: &exec.Cmd{
							Path: os.Args[0],
							Args: []string{os.Args[0]},
						},
						ExitCode: 128 + int(syscall.SIGINT),
						Err:      ErrSignal,
					}
				}
				count++
			}
			b.Submit(v)
		}
	}
}

// Supervise runs, monitors and restarts a list of commands.
func (o *Opt) Supervise(args ...*Cmd) error {
	b := broadcast.NewBroadcaster(len(args))
	defer func() {
		_ = b.Close()
	}()

	o.g.Go(func() error {
		return o.sighandler(o.ctx, b)
	})

	for _, v := range args {
		o.g.Go(func() error {
			for {
				err := o.run(b, v)

				select {
				case <-o.ctx.Done():
					return err
				default:
				}

				if err != nil && errors.Is(err.Err, context.Canceled) {
					return err
				}

				if rerr := o.retry(v, err); rerr != nil {
					return rerr
				}
			}
		})
	}

	return o.g.Wait()
}

type ExitError struct {
	Err      error
	Cmd      *exec.Cmd
	ExitCode int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("Exited with status %d: %s", e.ExitCode, e.Err.Error())
}

func (e *ExitError) String() string {
	return e.Cmd.String()
}

func (o *Opt) run(b broadcast.Broadcaster, argv *Cmd) *ExitError {
	cmd := exec.CommandContext(o.ctx, argv.Path, argv.Args[1:]...)
	cmd.Stdin = argv.Stdin
	cmd.Stdout = argv.Stdout
	cmd.Stderr = argv.Stderr
	cmd.Env = argv.Env
	cmd.Cancel = func() error {
		return o.cancelFunc(cmd, o.cancelSignal)
	}
	cmd.SysProcAttr = argv.SysProcAttr

	if err := cmd.Start(); err != nil {
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

	waitch := make(chan error, 1)
	go func() {
		waitch <- cmd.Wait()
	}()

	return o.waitpid(waitch, b, cmd)
}

func (o *Opt) waitpid(waitch <-chan error, b broadcast.Broadcaster, cmd *exec.Cmd) *ExitError {
	var ee *exec.ExitError

	ch := make(chan any)
	b.Register(ch)
	defer b.Unregister(ch)

	for {
		select {
		case v := <-ch:
			if sig, ok := v.(os.Signal); ok {
				_ = cmd.Process.Signal(sig)
			}
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
