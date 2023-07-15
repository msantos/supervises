package supervises_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"codeberg.org/msantos/supervises"
)

const (
	timeout = 5 * time.Second
)

func TestOpt_Cmd(t *testing.T) {
	s := supervises.New(context.Background())
	_, err := s.Cmd("cat", "cat", "nonexist-executable", "cat")
	if err == supervises.ErrInvalidCommand {
		t.Errorf("unexpected error: %v", err)
		return
	}
}

func TestOpt_Supervise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s := supervises.New(ctx)
	cmds, err := s.Cmd("cat", "cat", "cat")
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	if err := s.Supervise(cmds...); err != nil {
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
			t.Errorf("%v", err)
			return
		}

		if !errors.Is(ee.Err(), context.DeadlineExceeded) {
			t.Errorf("supervisor error: %s", ee.Error())
			return
		}

		if ee.ExitCode() != 126 {
			t.Errorf("unexpected exit status: %d: %s", ee.ExitCode(), ee.Error())
			return
		}

		return
	}

	t.Error("supervisor exited")
}

var ErrRetryAttemptsExceeded = errors.New("retry attempts exceeded")

type retryState struct {
	count int
}

func (r *retryState) retry(c *supervises.Cmd, cerr error) error {
	if r.count > 0 {
		return ErrRetryAttemptsExceeded
	}

	r.count++
	return nil
}

func TestOpt_Supervise_retry(t *testing.T) {
	r := &retryState{}
	s := supervises.New(
		context.Background(),
		supervises.WithRetry(r.retry),
	)
	cmds, err := s.Cmd("cat", "cat", "cat")
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	if err := s.Supervise(cmds...); err != nil {
		if !errors.Is(err, ErrRetryAttemptsExceeded) {
			t.Errorf("%v", err)
			return
		}

		return
	}

	t.Error("supervisor exited")
}

func ExampleOpt_Supervise() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	s := supervises.New(ctx)
	cmds, err := s.Cmd("@echo test123; exec cat", "cat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	err = s.Supervise(cmds...)

	var ee *supervises.ExitError

	if !errors.As(err, &ee) {
		fmt.Fprintln(os.Stderr, err.Error())
		return
	}

	if !errors.Is(ee.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "unexpected exit status: %d: %s", ee.ExitCode(), ee.Error())
		return
	}

	// Output: test123
}
