package verifier

// maxSandboxCaptureBytes caps each captured output stream (stdout and stderr)
// of a sandboxed gate command. A verbose command (go test -v, a noisy build)
// cannot grow verifier memory without bound; the retained tail still carries
// the runner's end-of-output failure summary.
const maxSandboxCaptureBytes = 1 << 20

type commandFailure struct {
	class    string
	detail   string
	failures []string
	err      error
}

func (e *commandFailure) Error() string { return e.err.Error() }
func (e *commandFailure) Unwrap() error { return e.err }

// boundedCapture is a tail-retaining output sink for sandboxed gate commands.
// Write keeps only the last max bytes of output, so an unboundedly verbose
// command cannot grow verifier memory without bound, and it always reports the
// full write length so the child process never blocks on a short write. The
// retained tail preserves the failure summary runners print at the end.
type boundedCapture struct {
	max int
	buf []byte
}

func newBoundedCapture(max int) *boundedCapture {
	return &boundedCapture{max: max}
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	if c == nil || c.max <= 0 {
		return len(p), nil
	}
	if len(p) >= c.max {
		c.buf = append(c.buf[:0], p[len(p)-c.max:]...)
		return len(p), nil
	}
	if len(c.buf)+len(p) > c.max {
		drop := len(c.buf) + len(p) - c.max
		c.buf = append(c.buf[:0], c.buf[drop:]...)
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// Bytes returns the retained tail. The caller must treat the result as
// read-only.
func (c *boundedCapture) Bytes() []byte {
	if c == nil {
		return nil
	}
	return c.buf
}
