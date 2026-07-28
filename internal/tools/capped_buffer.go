package tools

import "sync"

// cappedBuffer accepts all Write bytes (so child processes never block on a
// full pipe) but only retains the first max bytes for the tool result.
// This bounds agent RSS without tightening the product maxOut contract:
// returned content is still the same post-capture prefix + truncation notice.
// max <= 0 means retain everything (tests / explicit unlimited capture).
type cappedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	max       int
	written   int64
	truncated bool
}

func newCappedBuffer(max int) *cappedBuffer {
	return &cappedBuffer{max: max}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written += int64(len(p))
	if c.max <= 0 {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	if len(c.buf) >= c.max {
		c.truncated = true
		return len(p), nil
	}
	remain := c.max - len(c.buf)
	if len(p) <= remain {
		c.buf = append(c.buf, p...)
	} else {
		c.buf = append(c.buf, p[:remain]...)
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out
}

func (c *cappedBuffer) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

func (c *cappedBuffer) Written() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}
