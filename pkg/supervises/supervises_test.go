package supervises_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"go.iscode.ca/supervises/pkg/supervises"
)

const (
	timeout = 5 * time.Second
)

func Test_Parse(t *testing.T) {
	_, err := supervises.Parse("cat", "cat", "nonexist-executable", "cat")
	if err == supervises.ErrInvalidCommand {
		t.Errorf("unexpected error: %v", err)
		return
	}
}

func TestSupervisor_Run(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmds, err := supervises.Parse("cat", "cat", "cat")
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	sv := supervises.New(ctx, cmds, supervises.WithRetry(
		func(_ *supervises.Cmd, eerr *supervises.ExitError) *supervises.ExitError {
			if eerr == nil {
				return &supervises.ExitError{
					Err:      ErrRetryAttemptsExceeded,
					ExitCode: 0,
				}
			}
			return eerr
		},
	))

	if err := sv.Run(); err != nil {
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
			t.Errorf("%v", err)
			return
		}

		if ee != nil {
			switch ee.ExitCode {
			case 0:
			default:
				t.Errorf("unexpected exit status: %d: %s", ee.ExitCode, ee.Error())
				return
			}
		}

		return
	}

	t.Error("supervisor exited")
}

var ErrRetryAttemptsExceeded = errors.New("retry attempts exceeded")

type retryState struct {
	count int
}

func (r *retryState) retry(c *supervises.Cmd, ee *supervises.ExitError) *supervises.ExitError {
	if r.count > 0 {
		return &supervises.ExitError{
			Err:      ErrRetryAttemptsExceeded,
			ExitCode: 111,
		}
	}

	r.count++
	return nil
}

func TestSupervisor_Run_retry(t *testing.T) {
	cmds, err := supervises.Parse("@echo >/dev/null", "cat", "cat")
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	r := &retryState{}

	sv := supervises.New(context.Background(), cmds, supervises.WithRetry(r.retry))

	if err := sv.Run(); err != nil {
		var ee *supervises.ExitError
		if !errors.As(err, &ee) {
			t.Errorf("%v", err)
			return
		}
		if !errors.Is(ee.Err, ErrRetryAttemptsExceeded) {
			t.Errorf("%v", err)
			return
		}

		return
	}

	t.Error("supervisor exited")
}

type Stdout struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *Stdout) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *Stdout) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

var ErrExitSuccess = errors.New("exited")

func TestSupervisor_Run_stdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmds, err := supervises.Parse("cat", "cat", "cat")
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(len(cmds))

	sv := supervises.New(ctx, cmds,
		supervises.WithStdin(io.NopCloser(bytes.NewReader([]byte("testing 123\n")))),
		supervises.WithRetry(
			func(cmd *supervises.Cmd, _ *supervises.ExitError) *supervises.ExitError {
				wg.Done()
				wg.Wait()
				return &supervises.ExitError{
					Cmd: &exec.Cmd{
						Path: cmd.Path,
						Args: cmd.Args,
					},
					Err:      nil,
					ExitCode: 0,
				}
			},
		),
	)

	stdout := &Stdout{}

	for _, cmd := range cmds {
		cmd.Stdout = stdout
	}

	if err := sv.Run(); err != nil {
		var ee *supervises.ExitError

		if !errors.As(err, &ee) {
			t.Errorf("%v", err)
			return
		}

		if ee.ExitCode != 0 {
			t.Errorf("unexpected exit status: %d: %s", ee.ExitCode, ee.Error())
			return
		}
	}

	buf := stdout.Bytes()
	if !bytes.Equal(buf, []byte("testing 123\ntesting 123\ntesting 123\n")) {
		t.Errorf("unexpected output: %s", buf)
		return
	}
}

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
