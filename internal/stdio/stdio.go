package stdio

type Stdio int

const (
	none   Stdio = 0
	Stdout       = 1 << 0
	Stderr       = 1 << 1
)
