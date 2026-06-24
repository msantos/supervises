// The supervises utility is a command-line tool designed to run and monitor
// multiple subprocesses simultaneously. It supports signal forwarding,
// standard input broadcasting to all active child processes, and configurable
// Erlang-like supervisor restart strategies.
//
// # Sigils
//
// Subprocess commands are specified as command-line arguments. They can be prefixed
// with sigils to control how standard streams are handled:
//
//   - @: Executes the command in a shell (/bin/sh -c).
//     Example: supervises @'nc -l 8080 > nc.log'
//
//   - =: Discards standard input, standard output, and standard error.
//     Example: supervises ='nc -l 8080'
//
//   - =0: Discards standard input only.
//     Example: supervises =0'nc -l 8080'
//
//   - =1: Discards standard output only.
//     Example: supervises =1'nc -l 8080'
//
//   - =2: Discards standard error only.
//     Example: supervises =2'nc -l 8080'
//
//   - =3: Discards both standard output and standard error.
//     Example: supervises =3'nc -l 8080'
//
// # Restart Strategies
//
// The command-line supervisor supports various restart strategies configured via
// the -strategy flag:
//
//   - always: Restarts a command regardless of its exit status.
//
//   - on-error: Restarts a command only if it exits with a non-zero status.
//
//   - on-success: Restarts a command only if it exits with a zero status.
//
//   - one-for-all: If any supervised command fails, all other commands are
//     terminated, and they are not restarted unless configured otherwise.
//
//   - one-for-all-always: If any supervised command exits (successfully or with error),
//     all other commands are terminated.
//
//   - rest-for-one: If a supervised command fails, any commands started after it
//     (in the order defined on the command line) are terminated.
//
//   - rest-for-one-always: If a supervised command exits, any commands started
//     after it are terminated.
//
// # Signal Handling (Ctrl-C)
//
// The supervisor intercepts standard signals (such as SIGHUP, SIGQUIT, SIGTERM, etc.)
// and forwards them to all running subprocesses.
//
// Keyboard interrupts (Ctrl-C / SIGINT) receive special handling:
//   - First Ctrl-C: The supervisor intercepts the SIGINT and forwards it to all
//     supervised subprocesses, allowing them to perform cleanups or shut down gracefully.
//     The supervisor remains running.
//   - Second Ctrl-C: If sent within 1 second of the first, the supervisor forces immediate
//     termination of all subprocesses (sending the signal specified by the -signal flag,
//     which defaults to SIGKILL) and exits.
//
// # Standard Input (Stdin)
//
// By default, standard input sent to the supervisor is broadcast (replicated) to
// the standard input of all supervised subprocesses.
//
// When standard input is closed or reaches EOF (e.g. via Ctrl-D):
//   - The supervisor closes the stdin pipe for all supervised subprocesses.
//   - Subprocesses are not terminated; they continue running until they exit or the
//     supervisor is stopped.
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
	strategy := flag.String("strategy", "always", "restart strategy (always, on-error, on-success, one-for-all, rest-for-one, one-for-all-always, rest-for-one-always)")

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
	case "always", "on-error", "on-success", "one-for-all", "one_for_all", "one-for-all-always", "one_for_all_always", "rest-for-one", "rest_for_one", "rest-for-one-always", "rest_for_one_always":
	default:
		fmt.Fprintf(os.Stderr, "invalid strategy: %s (must be one of: always, on-error, on-success, one-for-all, rest-for-one, one-for-all-always, rest-for-one-always)\n", *strategy)
		usage()
		os.Exit(2)
	}

	cmds, err := supervises.Parse(flag.Args()...)
	if err != nil {
		l.Info("parse error", "error", err)
		os.Exit(2)
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

	ctx, cancel := context.WithCancel(context.Background())

	mgr := &StrategyManager{
		ctx:           ctx,
		strategy:      *strategy,
		cmds:          cmds,
		states:        states,
		restartCount:  *restartCount,
		restartPeriod: *restartPeriod,
		restartWait:   *restartWait,
		errExit:       *errExit,
		signal:        syscall.Signal(*sig),
		logger:        l,
	}

	var e *supervises.ExitError

	sv := supervises.New(ctx, cmds,
		supervises.WithOnExit(mgr.OnExit),
		supervises.WithOnStart(mgr.OnStart),
	)

	supervises.ForwardSignals(ctx, sv, slices.DeleteFunc(supervises.DefaultSignals, func(sig os.Signal) bool { return sig == syscall.SIGINT })...)

	sigintCh := make(chan os.Signal, 1)
	signal.Notify(sigintCh, syscall.SIGINT)

	go func() {
		var lastCtrlC time.Time
		cooldown := 1 * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigintCh:
				now := time.Now()
				if !lastCtrlC.IsZero() && now.Sub(lastCtrlC) < cooldown {
					l.Info("Interrupt received again within cooldown, terminating supervisor...")
					cancel()
					return
				}
				lastCtrlC = now
				l.Info("Interrupt received, forwarding SIGINT to supervised processes. Repeat within 1s to terminate supervisor.")
				sv.SignalAll(syscall.SIGINT)
			}
		}
	}()

	if err := sv.Run(); err != nil {
		if !errors.As(err, &e) {
			l.Debug("command exited", "error", err)
			os.Exit(128)
		}

		l.Debug("command exited", "argv", e.String(), "status", e.ExitCode, "error", e.Err)
		os.Exit(e.ExitCode)
	}

	if exitErr := mgr.FirstFailedError(); exitErr != nil {
		l.Debug("command exited", "argv", exitErr.String(), "status", exitErr.ExitCode, "error", exitErr.Err)
		os.Exit(exitErr.ExitCode)
	}
}
