# SYNOPSIS

supervises [*options*] "*command* *...*" "..."

# DESCRIPTION

Minimal command line supervisor.

# BUILDING

```
go install codeberg.org/msantos/supervises/cmd/supervises@latest
```

## Source

```
cd cmd/supervises
CGO_ENABLED=0 go build -trimpath -ldflags "-w"
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

* =: discard stdout/stderr

  ```
     # equivalent to: supervises @'nc -l 8080 >/dev/null 2>&1'
     supervises ='nc -l 8080'
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

# OPTIONS

help
: Display usage

signal *int*
: signal sent to subprocesses on exit (default 9)

verbose
: Enable debug messages

errexit
: retries apply to tasks exiting with a non-0 status

retry-count int
: retry limit before exiting (0: no limit)

retry-period duration
: time interval for retries (0: no limit)

retry-wait duration
: retry backoff interval (default 1s)
