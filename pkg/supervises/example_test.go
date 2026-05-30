package supervises_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

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
