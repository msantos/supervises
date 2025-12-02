package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sync/atomic"
	"syscall"
	"time"

	"go.iscode.ca/supervises/pkg/supervises"
)

const (
	version = "0.7.0"
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

	retryWait := flag.Duration("retry-wait", 1*time.Second, "retry backoff interval")
	retryCount := flag.Int("retry-count", 0, "retry limit before exiting (0: no limit)")
	retryPeriod := flag.Duration("retry-period", 0, "time interval for retries (0: no limit)")
	errExit := flag.Bool("errexit", false, "retries apply to tasks exiting with a non-0 status")

	flag.Usage = func() { usage() }
	flag.Parse()

	if *verbose {
		programLevel.Set(slog.LevelDebug)
	}

	var count atomic.Int32

	count.Store(int32(*retryCount))

	var t *time.Ticker

	if *retryPeriod > 0 {
		t = time.NewTicker(*retryPeriod)
	}

	retry := func(c *supervises.Cmd, ee *supervises.ExitError) *supervises.ExitError {
		if ee != nil {
			l.Debug("command failed", "argv", ee.String(), "status", ee.ExitCode, "error", ee.Err)
		}

		if *retryCount > 0 {
			if t != nil {
				select {
				case <-t.C:
					count.Store(int32(*retryCount))
				default:
				}
			}

			if *errExit {
				if ee != nil {
					count.Add(-1)
				}
			} else {
				count.Add(-1)
			}

			if count.Load() <= 0 {
				if ee != nil {
					return ee
				}
				return &supervises.ExitError{
					Argv: c.String(),
				}
			}
		}

		time.Sleep(*retryWait)
		return nil
	}

	if *help || flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	s := supervises.New(
		context.Background(),
		supervises.WithCancelSignal(syscall.Signal(*sig)),
		supervises.WithRetry(retry),
	)

	var ee *supervises.ExitError

	cmd, err := s.Cmd(flag.Args()...)
	if err != nil {
		if !errors.As(err, &ee) {
			l.Debug("command failed", "error", err)
			os.Exit(126)
		}

		l.Error("command failed", "argv", ee.String(), "status", ee.ExitCode, "error", ee.Err)
		os.Exit(ee.ExitCode)
	}

	if err := s.Supervise(cmd...); err != nil {
		if !errors.As(err, &ee) {
			l.Debug("command failed", "error", err)
			os.Exit(128)
		}

		l.Debug("command failed", "argv", ee.String(), "status", ee.ExitCode, "error", ee.Err)
		os.Exit(ee.ExitCode)
	}
}
