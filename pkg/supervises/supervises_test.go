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

	s := supervises.New(ctx, supervises.WithRetry(
		func(_ *supervises.Cmd, eerr *supervises.ExitError) *supervises.ExitError {
			return eerr
		},
	))
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

		if ee != nil {
			switch ee.ExitCode {
			// The exit code returned will depend on which call was interruped by the cancelled context.
			case 126, 128, 137:
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

func TestOpt_Supervise_retry(t *testing.T) {
	r := &retryState{}
	s := supervises.New(
		context.Background(),
		supervises.WithRetry(r.retry),
	)
	cmds, err := s.Cmd("@echo >/dev/null", "cat", "cat")
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

func TestOpt_Supervise_stdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//supervised := []string{"@read line; echo $line", "@read line; echo $line", "@read line; echo $line"}
	supervised := []string{"cat", "cat", "cat"}
	var wg sync.WaitGroup
	wg.Add(len(supervised))

	s := supervises.New(ctx,
		supervises.WithStdin(io.NopCloser(bytes.NewReader([]byte("testing 123\n")))),
		supervises.WithRetry(
			func(_ *supervises.Cmd, _ *supervises.ExitError) *supervises.ExitError {
				wg.Done()
				wg.Wait()
				return &supervises.ExitError{
					Cmd: &exec.Cmd{
						Path: "cat",
						Args: []string{"cat"},
					},
					Err:      nil,
					ExitCode: 0,
				}
			},
		),
	)

	cmds, err := s.Cmd(supervised...)
	if err != nil {
		t.Errorf("invalid command: %v", err)
		return
	}

	stdout := &Stdout{}

	for _, cmd := range cmds {
		cmd.Stdout = stdout
	}

	if err := s.Supervise(cmds...); err != nil {
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

func ExampleOpt_Supervise() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	s := supervises.New(ctx)
	cmds, err := s.Cmd("@echo test123; exec sleep 10", "cat")
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

	// Output: test123
}
