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
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"
```

# EXAMPLES

```
supervises 'nc -vnl 7070' 'nc -vnl 7071' 'nc -vnl 7072'
```

# OPTIONS

help
: Display usage

signal *int*
: signal sent to subprocesses on exit (default 9)

verbose
: Enable debug messages
