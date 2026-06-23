[![Go Reference](https://pkg.go.dev/badge/go.iscode.ca/supervises.svg)](https://pkg.go.dev/go.iscode.ca/supervises)

# SYNOPSIS

supervises [*options*] "*command* *...*" "..."

# DESCRIPTION

Minimal command line supervisor.

# BUILDING

```
go install go.iscode.ca/supervises/cmd/supervises@latest
```

## Source

```
CGO_ENABLED=0 go build -trimpath -ldflags "-w" ./cmd/supervises
```

# EXAMPLES

```
supervises 'nc -vnl 7070' 'nc -vnl 7071' 'nc -vnl 7072'
```

# SIGILS

* @: run in shell

  ```
  supervises @'nc -l 8080 >nc.log'
  ```

* =: discard stdin, stdout and stderr

  ```
  # equivalent to: supervises @'nc -l 8080 </dev/null >/dev/null 2>&1'
  supervises ='nc -l 8080'
  ```

* =0: discard stdin

  ```
  # equivalent to: supervises @'nc -l 8080 </dev/null'
  supervises =0'nc -l 8080'
  ```

* =1: discard stdout

  ```
  # equivalent to: supervises @'nc -l 8080 >/dev/null'
  supervises =1'nc -l 8080'
  ```

* =2: discard stderr

  ```
  # equivalent to: supervises @'nc -l 8080 2>/dev/null'
  supervises =2'nc -l 8080'
  ```

* =3: discard stdout and stderr

  ```
  # equivalent to: supervises @'nc -l 8080 >/dev/null 2>&1'
  supervises =3'nc -l 8080'
  ```

# OPTIONS

help
: Display usage

signal *int*
: signal sent to subprocesses on exit (default 9)

verbose
: Enable debug messages

errexit
: restarts apply to tasks exiting with a non-0 status

strategy *string*
: restart strategy (default: always):
  * **always**: restart the process regardless of its exit status.
  * **on-error**: restart the process only if it exits with a non-zero status. If a process exits with status 0, it is not restarted, but other processes remain running.
  * **on-success**: restart the process only if it exits with a zero status. If a process exits with a non-zero status, it is not restarted, but other processes remain running.
  * **one-for-all** (or `one_for_all`): if a process crashes (exits non-zero), all other processes are terminated and restarted. If a process exits with status 0, it is not restarted, but other processes remain running (transient behavior).
  * **one-for-all-always** (or `one_for_all_always`): if any process exits (normally or via crash), all other processes are terminated, and all processes are restarted (permanent behavior).
  * **rest-for-one** (or `rest_for_one`): if a process crashes (exits non-zero), all processes started after it (in command-line order) are terminated and restarted. If a process exits with status 0, it is not restarted, but other processes remain running (transient behavior).
  * **rest-for-one-always** (or `rest_for_one_always`): if any process exits, all processes started after it are terminated, and all affected processes are restarted (permanent behavior).

restart-count int
: restart limit before exiting (0: no limit)

restart-period duration
: time interval for restarts (0: no limit)

restart-wait duration
: restart backoff interval (default 1s)

# SIGNAL HANDLING (CTRL-C)

`supervises` intercepts standard OS signals (such as `SIGHUP`, `SIGQUIT`, `SIGTERM`, etc.) and forwards them to all running subprocesses.

Keyboard interrupts (`Ctrl-C` / `SIGINT`) receive special handling:
- **First Ctrl-C**: The supervisor intercepts the `SIGINT` and forwards it to all supervised subprocesses, allowing them to clean up or shut down gracefully. The supervisor continues running.
- **Second Ctrl-C**: If sent within 1 second of the first, the supervisor forces immediate termination of all subprocesses (sending the signal specified by `-signal`, which defaults to `SIGKILL` / `9`) and exits.

# STANDARD INPUT (STDIN)

By default, standard input sent to the supervisor is broadcast (replicated) to the standard input of all supervised subprocesses.

When standard input is closed or reaches EOF (e.g. via `Ctrl-D`):
- The supervisor closes the stdin pipe for all supervised subprocesses.
- The subprocesses themselves are not terminated; they continue running until they exit or the supervisor is stopped.

# LIBRARY USAGE

`supervises` can be used as a Go library to programmatically manage and monitor multiple subprocesses:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.iscode.ca/supervises/pkg/supervises"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Parse command strings (supporting sigil prefixes)
	cmds, err := supervises.Parse("@echo 'starting supervises...';", "sleep 10")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		return
	}

	// Create a new supervisor with options
	sv := supervises.New(ctx, cmds,
		supervises.WithOnStart(func(cmd *supervises.Cmd, pid int) {
			fmt.Printf("Command %s started with PID %d\n", cmd.Path, pid)
		}),
		supervises.WithOnExit(func(cmd *supervises.Cmd, err *supervises.ExitError) *supervises.ExitError {
			if err != nil {
				fmt.Printf("Command %s exited with error: %v\n", cmd.Path, err)
				return err // Return err to propagate and stop monitoring
			}
			return nil // Returning nil restarts the process (default 1s sleep)
		}),
	)

	// Intercept and forward standard OS signals to all supervised processes
	supervises.ForwardSignals(ctx, sv, supervises.DefaultSignals...)

	// Run starts, monitors, and blocks until context cancellation or fatal exit
	if err := sv.Run(); err != nil {
		var e *supervises.ExitError
		if errors.As(err, &e) {
			fmt.Fprintf(os.Stderr, "Process exited with code %d\n", e.ExitCode)
		} else {
			fmt.Fprintf(os.Stderr, "Supervisor failed: %v\n", err)
		}
	}
}
```
