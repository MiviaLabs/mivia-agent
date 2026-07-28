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

// dualCapture splits one product maxOut budget across stdout and stderr so
// peak retained capture is ≤ max (not 2×max). Writers still accept all bytes.
type dualCapture struct {
	mu        sync.Mutex
	max       int
	used      int
	stdout    []byte
	stderr    []byte
	written   int64
	truncated bool
}

func newDualCapture(max int) *dualCapture {
	return &dualCapture{max: max}
}

func (d *dualCapture) Stdout() *streamSide { return &streamSide{d: d, out: true} }
func (d *dualCapture) Stderr() *streamSide { return &streamSide{d: d, out: false} }

type streamSide struct {
	d   *dualCapture
	out bool
}

func (s *streamSide) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.d.written += int64(len(p))
	if s.d.max <= 0 {
		if s.out {
			s.d.stdout = append(s.d.stdout, p...)
		} else {
			s.d.stderr = append(s.d.stderr, p...)
		}
		return len(p), nil
	}
	if s.d.used >= s.d.max {
		s.d.truncated = true
		return len(p), nil
	}
	remain := s.d.max - s.d.used
	take := p
	if len(p) > remain {
		take = p[:remain]
		s.d.truncated = true
	}
	if s.out {
		s.d.stdout = append(s.d.stdout, take...)
	} else {
		s.d.stderr = append(s.d.stderr, take...)
	}
	s.d.used += len(take)
	return len(p), nil
}

func (d *dualCapture) Combined() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]byte, 0, len(d.stdout)+len(d.stderr))
	out = append(out, d.stdout...)
	out = append(out, d.stderr...)
	return string(out)
}

func (d *dualCapture) Truncated() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.truncated
}

func (d *dualCapture) Retained() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.used
}

func (d *dualCapture) Written() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.written
}
