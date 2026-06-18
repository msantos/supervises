package supervises_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"

	"go.iscode.ca/supervises/pkg/supervises"
)

func ExampleSupervisor_Run() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmds, err := supervises.Parse("@echo test123; exec sleep 10", "cat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	sv := supervises.New(ctx, cmds)

	if err = sv.Run(); err != nil {
		var e *supervises.ExitError

		if !errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}

func ExampleSupervisor_Run_signal_handler() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmds, err := supervises.Parse("@echo test123; exec sleep 10", "cat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	sv := supervises.New(ctx, cmds)

	supervises.ForwardSignals(ctx, sv, supervises.DefaultSignals...)

	if err = sv.Run(); err != nil {
		var e *supervises.ExitError

		if !errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}

func ExampleSupervisor_Run_onStart() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmds, err := supervises.Parse("@echo test123; exec sleep 10", "cat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	// Safely signal supervised processes.
	sv := supervises.New(ctx, cmds, supervises.WithOnStart(func(cmd *supervises.Cmd, pid int) {
		pidfd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			log.Printf("PidfdOpen failed: %v", err)
			return
		}

		go func() {
			defer func() {
				_ = unix.Close(pidfd)
			}()

			time.Sleep(5 * time.Second)

			err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0)
			if err != nil {
				log.Printf("PidfdSendSignal failed: %v", err)
			}
		}()
	}))

	supervises.ForwardSignals(ctx, sv, supervises.DefaultSignals...)

	if err = sv.Run(); err != nil {
		var e *supervises.ExitError

		if !errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}

func ExampleSupervisor_Run_custom_signal_handler() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmds, err := supervises.Parse("@echo test123; exec sleep 10", "cat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	sv := supervises.New(ctx, cmds)

	// Custom signal handler:
	// - 1st ctrl-C (SIGINT): ignored
	// - 2nd ctrl-C (SIGINT): sends SIGINT to supervised processes
	// - 3rd ctrl-C (SIGINT): sends SIGKILL to supervised processes
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, unix.SIGINT)

	go func() {
		defer signal.Stop(sigch)
		count := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigch:
				count++
				switch count {
				case 1:
					// 1st ctrl-C: ignored
				case 2:
					// 2nd ctrl-C: sends SIGINT to supervised processes
					sv.SignalAll(unix.SIGINT)
				case 3:
					// 3rd ctrl-C: sends SIGKILL to supervised processes
					sv.SignalAll(unix.SIGKILL)
					return
				}
			}
		}
	}()

	if err = sv.Run(); err != nil {
		var e *supervises.ExitError

		if !errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}
