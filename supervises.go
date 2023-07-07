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
	ErrExitFailure    = errors.New("failed")
	ErrInvalidCommand = errors.New("invalid command")
	ErrSignal         = errors.New("terminated by signal")
)

type Opt struct {
	uctx         context.Context
	ctx          context.Context
	cancel       context.CancelFunc
	g            *errgroup.Group
	cancelSignal syscall.Signal
	signals      []os.Signal
	log          func(s ...string)
}

type Option func(*Opt)

// WithLog sets the debug logger.
func WithLog(log func(s ...string)) Option {
	return func(o *Opt) {
		if log != nil {
			o.log = log
		}
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

// New returns configuration for supervisors.
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
		log: func(s ...string) {},
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
	var stdout io.Writer = os.Stdout
	var stderr io.Writer = os.Stderr

	switch {
	case strings.HasPrefix(arg, "@"):
		return &Cmd{Path: "/bin/sh", Args: []string{"-c", strings.TrimPrefix(arg, "@")}}, nil
	case strings.HasPrefix(arg, "=1"):
		stdout = io.Discard
		arg = strings.TrimPrefix(arg, "=1")
	case strings.HasPrefix(arg, "=2"):
		stderr = io.Discard
		arg = strings.TrimPrefix(arg, "=2")
	case strings.HasPrefix(arg, "="):
		stdout = io.Discard
		stderr = io.Discard
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

	return &Cmd{
		Path:   arg0,
		Args:   argv[1:],
		Env:    os.Environ(),
		Stdin:  os.Stdin,
		Stdout: stdout,
		Stderr: stderr,
		SysProcAttr: &syscall.SysProcAttr{
			Pdeathsig: syscall.SIGKILL,
		},
	}, nil
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
					return ErrSignal
				}
				count++
			}
			b.Submit(v)
		}
	}
}

func (o *Opt) Supervise(args ...*Cmd) error {
	b := broadcast.NewBroadcaster(len(args))
	defer b.Close()

	o.g.Go(func() error {
		return o.sighandler(o.ctx, b)
	})

	for _, v := range args {
		v := v
		o.g.Go(func() error {
			for {
				err := o.run(b, v)
				select {
				case <-o.ctx.Done():
					return err
				default:
				}
				if err != nil {
					var ee *ExitError
					e := err
					if errors.As(err, &ee) {
						e = ee.Err()
					}
					if errors.Is(e, exec.ErrNotFound) || errors.Is(e, context.Canceled) {
						return err
					}

					o.log("argv", v.String(), "error", err.Error())
				}
				time.Sleep(time.Second)
			}
		})
	}

	return o.g.Wait()
}

type ExitError struct {
	err    error
	cmd    *exec.Cmd
	status int
}

func (e *ExitError) Err() error {
	return e.err
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("Exited with status %d: %s", e.status, e.err.Error())
}

func (e *ExitError) ExitCode() int {
	return e.status
}

func (e *ExitError) Argv() []string {
	return append([]string{e.cmd.Path}, e.cmd.Args...)
}

func (o *Opt) run(b broadcast.Broadcaster, argv *Cmd) error {
	cmd := exec.CommandContext(o.ctx, argv.Path, argv.Args...)
	cmd.Stdin = argv.Stdin
	cmd.Stdout = argv.Stdout
	cmd.Stderr = argv.Stderr
	cmd.Env = argv.Env
	cmd.Cancel = func() error {
		return cmd.Process.Signal(o.cancelSignal)
	}
	cmd.SysProcAttr = argv.SysProcAttr

	if err := cmd.Start(); err != nil {
		status := cmd.ProcessState.ExitCode()
		if status < 0 {
			status = 126
		}
		return &ExitError{
			cmd:    cmd,
			err:    err,
			status: status,
		}
	}

	waitch := make(chan error, 1)
	go func() {
		waitch <- cmd.Wait()
	}()

	return o.waitpid(waitch, b, cmd)
}

func (o *Opt) waitpid(waitch <-chan error, b broadcast.Broadcaster, cmd *exec.Cmd) error {
	var ee *exec.ExitError

	ch := make(chan interface{})
	b.Register(ch)
	defer b.Unregister(ch)

	for {
		select {
		case v := <-ch:
			var sig os.Signal
			switch v := v.(type) {
			case os.Signal:
				sig = v
			default:
				continue
			}
			_ = cmd.Process.Signal(sig)
		case err := <-waitch:
			if err == nil {
				return nil
			}

			if !errors.As(err, &ee) {
				return &ExitError{
					cmd:    cmd,
					status: 128,
					err:    err,
				}
			}

			waitStatus, ok := ee.Sys().(syscall.WaitStatus)
			if !ok {
				return &ExitError{
					cmd:    cmd,
					status: 128,
					err:    err,
				}
			}

			if waitStatus.Signaled() {
				return &ExitError{
					cmd:    cmd,
					status: 128 + int(waitStatus.Signal()),
					err:    err,
				}
			}

			return &ExitError{
				cmd:    cmd,
				status: waitStatus.ExitStatus(),
				err:    ErrExitFailure,
			}
		}
	}
}
