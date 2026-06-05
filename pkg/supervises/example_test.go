package supervises_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}

func ExampleSupervisor_Run_signal_handling() {
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
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
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

			time.Sleep(1 * time.Second)

			err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0)
			if err != nil {
				log.Printf("PidfdSendSignal failed: %v", err)
			}
		}()
	}))

	supervises.ForwardSignals(ctx, sv, supervises.DefaultSignals...)

	if err = sv.Run(); err != nil {
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
	}

	// Output: test123
}
