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
	b            broadcast.Broadcaster
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

	return o
}

func (o *Opt) sighandler(ctx context.Context) error {
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
			o.b.Submit(v)
		}
	}
}

func (o *Opt) Supervise(ctx context.Context, args []string) error {
	o.b = broadcast.NewBroadcaster(len(args))
	defer o.b.Close()

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return o.sighandler(ctx)
	})

	for _, v := range args {
		v := v
		g.Go(func() error {
			argv, err := o.argv(v)
			if err != nil {
				return &ExitError{
					err:    err,
					status: 2,
					argv:   argv.argv,
				}
			}
			for {
				err := o.run(ctx, argv)
				select {
				case <-ctx.Done():
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

					o.log("argv", argv.String(), "error", err.Error())
				}
				time.Sleep(time.Second)
			}
		})
	}

	return g.Wait()
}

type Argv struct {
	argv []string
	out  io.Writer
	err  io.Writer
}

func (argv Argv) String() string {
	return strings.Join(argv.argv, " ")
}

func (o *Opt) argv(v string) (Argv, error) {
	cmd := Argv{
		out: os.Stdout,
		err: os.Stderr,
	}

	switch {
	case strings.HasPrefix(v, "@"):
		cmd.argv = []string{"/bin/sh", "-c", strings.TrimPrefix(v, "@")}
		return cmd, nil
	case strings.HasPrefix(v, "=1"):
		cmd.out = io.Discard
		v = strings.TrimPrefix(v, "=1")
	case strings.HasPrefix(v, "=2"):
		cmd.err = io.Discard
		v = strings.TrimPrefix(v, "=2")
	case strings.HasPrefix(v, "="):
		cmd.out = io.Discard
		cmd.err = io.Discard
		v = strings.TrimPrefix(v, "=")
	}
	if len(v) == 0 {
		return Argv{}, ErrInvalidCommand
	}
	argv, err := shellwords.Parse(v)
	if err != nil {
		return Argv{}, err
	}
	cmd.argv = argv
	return cmd, nil
}

type ExitError struct {
	err    error
	argv   []string
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
	return e.argv
}

func (o *Opt) run(ctx context.Context, argv Argv) error {
	arg0, err := exec.LookPath(argv.argv[0])
	if err != nil {
		return &ExitError{
			argv:   argv.argv,
			err:    err,
			status: 127,
		}
	}

	cmd := exec.CommandContext(ctx, arg0, argv.argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = argv.out
	cmd.Stderr = argv.err
	cmd.Env = os.Environ()
	cmd.Cancel = func() error {
		return cmd.Process.Signal(o.cancelSignal)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}

	if err := cmd.Start(); err != nil {
		status := cmd.ProcessState.ExitCode()
		if status < 0 {
			status = 126
		}
		return &ExitError{
			argv:   argv.argv,
			err:    err,
			status: status,
		}
	}

	waitch := make(chan error, 1)
	go func() {
		waitch <- cmd.Wait()
	}()

	return o.waitpid(waitch, cmd, argv.argv)
}

func (o *Opt) waitpid(waitch <-chan error, cmd *exec.Cmd, argv []string) error {
	var ee *exec.ExitError

	ch := make(chan interface{})
	o.b.Register(ch)
	defer o.b.Unregister(ch)

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
					argv:   argv,
					status: 128,
					err:    err,
				}
			}

			waitStatus, ok := ee.Sys().(syscall.WaitStatus)
			if !ok {
				return &ExitError{
					argv:   argv,
					status: 128,
					err:    err,
				}
			}

			if waitStatus.Signaled() {
				return &ExitError{
					argv:   argv,
					status: 128 + int(waitStatus.Signal()),
					err:    err,
				}
			}

			return &ExitError{
				argv:   argv,
				status: waitStatus.ExitStatus(),
				err:    ErrExitFailure,
			}
		}
	}
}
