package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-broadcast"
	"github.com/mattn/go-shellwords"
	"golang.org/x/exp/slog"
	"golang.org/x/sync/errgroup"
)

type state struct {
	l      *slog.Logger
	signal syscall.Signal
	b      broadcast.Broadcaster
	cancel context.CancelFunc
}

const (
	version = "0.2.0"
)

var (
	programLevel = new(slog.LevelVar)

	ErrExitFailure    = errors.New("failed")
	ErrInvalidCommand = errors.New("invalid command")
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

	flag.Usage = func() { usage() }
	flag.Parse()

	if *verbose {
		programLevel.Set(slog.LevelDebug)
	}

	if *help || flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	s := &state{
		signal: syscall.Signal(*sig),
		b:      broadcast.NewBroadcaster(flag.NArg()),
		l:      l,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go s.sighandler(cancel)

	if err := s.supervise(ctx, flag.Args()); err != nil {
		var ee *exitError

		if !errors.As(err, &ee) {
			l.Debug("command failed", "error", err)
			os.Exit(128)
		}

		l.Debug("command failed", "argv", ee.Argv(), "status", ee.ExitCode(), "error", err)
		os.Exit(ee.ExitCode())
	}
}

func (s *state) sighandler(cancel context.CancelFunc) {
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGALRM,
		syscall.SIGTERM,
		syscall.SIGUSR1,
		syscall.SIGUSR2,
	)
	defer signal.Stop(sigch)

	count := 0
	for v := range sigch {
		if v == syscall.SIGINT {
			if count > 0 {
				cancel()
				return
			}
			count++
		}
		s.b.Submit(v)
	}
}

func (s *state) supervise(ctx context.Context, args []string) error {
	g, ctx := errgroup.WithContext(ctx)

	for _, v := range args {
		v := v
		g.Go(func() error {
			argv, err := s.argv(v)
			if err != nil {
				return &exitError{
					err:    err,
					status: 2,
					argv:   argv.argv,
				}
			}
			for {
				err := s.run(ctx, argv)
				select {
				case <-ctx.Done():
					return err
				default:
				}
				if err != nil {
					var ee *exitError
					e := err
					if errors.As(err, &ee) {
						e = ee.Err()
					}
					if errors.Is(e, exec.ErrNotFound) || errors.Is(e, context.Canceled) {
						return err
					}

					s.l.Debug("command failed", "argv", argv, "error", err)
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

func (s *state) argv(v string) (Argv, error) {
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

type exitError struct {
	err    error
	argv   []string
	status int
}

func (e *exitError) Err() error {
	return e.err
}

func (e *exitError) Error() string {
	return fmt.Sprintf("Exited with status %d: %s", e.status, e.err.Error())
}

func (e *exitError) ExitCode() int {
	return e.status
}

func (e *exitError) Argv() []string {
	return e.argv
}

func (s *state) run(ctx context.Context, argv Argv) error {
	arg0, err := exec.LookPath(argv.argv[0])
	if err != nil {
		return &exitError{
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
		return cmd.Process.Signal(s.signal)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}

	if err := cmd.Start(); err != nil {
		status := cmd.ProcessState.ExitCode()
		if status < 0 {
			status = 126
		}
		return &exitError{
			argv:   argv.argv,
			err:    err,
			status: status,
		}
	}

	waitch := make(chan error, 1)
	go func() {
		waitch <- cmd.Wait()
	}()

	return s.waitpid(waitch, cmd, argv.argv)
}

func (s *state) waitpid(waitch <-chan error, cmd *exec.Cmd, argv []string) error {
	var ee *exec.ExitError

	ch := make(chan interface{})
	s.b.Register(ch)
	defer s.b.Unregister(ch)

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
				return &exitError{
					argv:   argv,
					status: 128,
					err:    err,
				}
			}

			waitStatus, ok := ee.Sys().(syscall.WaitStatus)
			if !ok {
				return &exitError{
					argv:   argv,
					status: 128,
					err:    err,
				}
			}

			if waitStatus.Signaled() {
				return &exitError{
					argv:   argv,
					status: 128 + int(waitStatus.Signal()),
					err:    err,
				}
			}

			return &exitError{
				argv:   argv,
				status: waitStatus.ExitStatus(),
				err:    ErrExitFailure,
			}
		}
	}
}
