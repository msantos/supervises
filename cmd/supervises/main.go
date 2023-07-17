package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"syscall"

	"codeberg.org/msantos/supervises"
	"golang.org/x/exp/slog"
)

const (
	version = "0.3.0"
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

	flag.Usage = func() { usage() }
	flag.Parse()

	log := func(s ...string) {}
	if *verbose {
		programLevel.Set(slog.LevelDebug)
		log = func(s ...string) {
			a := make([]any, 0, len(s))
			for _, v := range s {
				a = append(a, v)
			}
			l.Debug("supervises", a...)
		}
	}

	if *help || flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	s := supervises.New(
		context.Background(),
		supervises.WithCancelSignal(syscall.Signal(*sig)),
		supervises.WithLog(log),
	)

	var ee *supervises.ExitError

	cmd, err := s.Cmd(flag.Args()...)
	if err != nil {
		if !errors.As(err, &ee) {
			l.Debug("command failed", "error", err)
			os.Exit(126)
		}

		l.Error("command failed", "argv", ee.String(), "status", ee.ExitCode(), "error", err)
		os.Exit(ee.ExitCode())
	}

	if err := s.Supervise(cmd...); err != nil {
		if !errors.As(err, &ee) {
			l.Debug("command failed", "error", err)
			os.Exit(128)
		}

		l.Debug("command failed", "argv", ee.String(), "status", ee.ExitCode(), "error", err)
		os.Exit(ee.ExitCode())
	}
}
