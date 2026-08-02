package tools

import (
	"sync"
)

// captureElisionMarker sits between retained head and tail when a bound drops
// the middle of a stream. Compilers print errors last; keeping the tail is the
// reason dualCapture is not head-only.
const captureElisionMarker = "\n... [middle of output elided] ...\n"

// cappedBuffer accepts all Write bytes (so child processes never block on a
// full pipe) but only retains a head+tail window under max for the tool result.
// max <= 0 means retain everything (tests / explicit unlimited capture).
// Under a positive max: ~1/3 head + 2/3 tail with an elision marker when the
// middle is dropped.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       []byte // used only when max <= 0 (unlimited)
	head      []byte
	tail      []byte // ring: last tailQuota bytes
	max       int
	headQuota int
	tailQuota int
	written   int64
	truncated bool
}

func newCappedBuffer(max int) *cappedBuffer {
	c := &cappedBuffer{max: max}
	if max > 0 {
		c.headQuota, c.tailQuota = splitCaptureBudget(max)
	}
	return c
}

func splitCaptureBudget(max int) (headQuota, tailQuota int) {
	if max <= 0 {
		return 0, 0
	}
	// ~1/3 head, ~2/3 tail. Tiny budgets still get a non-zero tail when possible.
	headQuota = max / 3
	if headQuota < 1 && max >= 2 {
		headQuota = 1
	}
	tailQuota = max - headQuota
	return headQuota, tailQuota
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
	writeHeadTail(&c.head, &c.tail, &c.truncated, c.headQuota, c.tailQuota, p)
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max <= 0 {
		out := make([]byte, len(c.buf))
		copy(out, c.buf)
		return out
	}
	return assembleHeadTail(c.head, c.tail, c.truncated)
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
// peak retained capture is ≤ max (not 2×max). Writers still accept all bytes
// without retaining them (process is not stalled on a full pipe).
// Under a positive max: shared ~1/3 head + 2/3 chronological tail ring so late
// failure lines (typically stderr) survive when early stdout flooded the pipe.
type dualCapture struct {
	mu         sync.Mutex
	max        int
	headQuota  int
	tailQuota  int
	headUsed   int
	stdoutHead []byte
	stderrHead []byte
	// Fixed-capacity chronological ring (no per-write heap growth under flood).
	ring        []byte
	ringOut     []bool // parallel stream tag: true = stdout
	ringStart   int
	ringLen     int
	written     int64
	truncated   bool
	stdoutElide bool
	stderrElide bool
}

func newDualCapture(max int) *dualCapture {
	d := &dualCapture{max: max}
	if max > 0 {
		d.headQuota, d.tailQuota = splitCaptureBudget(max)
		if d.tailQuota > 0 {
			d.ring = make([]byte, d.tailQuota)
			d.ringOut = make([]bool, d.tailQuota)
		}
	}
	return d
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
			s.d.stdoutHead = append(s.d.stdoutHead, p...)
		} else {
			s.d.stderrHead = append(s.d.stderrHead, p...)
		}
		return len(p), nil
	}
	s.d.writeBounded(s.out, p)
	return len(p), nil
}

func (d *dualCapture) writeBounded(out bool, p []byte) {
	for len(p) > 0 {
		if d.headUsed < d.headQuota {
			take := d.headQuota - d.headUsed
			if take > len(p) {
				take = len(p)
			}
			if out {
				d.stdoutHead = append(d.stdoutHead, p[:take]...)
			} else {
				d.stderrHead = append(d.stderrHead, p[:take]...)
			}
			d.headUsed += take
			p = p[take:]
			continue
		}
		// Head full: remaining bytes go into the fixed tail ring only.
		d.truncated = true
		if out {
			d.stdoutElide = true
		} else {
			d.stderrElide = true
		}
		if d.tailQuota <= 0 {
			return
		}
		d.pushTail(out, p)
		return
	}
}

// pushTail writes p into the fixed ring, dropping the oldest bytes as needed.
// Peak retained bytes never exceed tailQuota; no per-write heap growth.
func (d *dualCapture) pushTail(out bool, p []byte) {
	// If this write alone is larger than the ring, only the last tailQuota matter.
	if len(p) >= d.tailQuota {
		copy(d.ring, p[len(p)-d.tailQuota:])
		for i := range d.ringOut {
			d.ringOut[i] = out
		}
		d.ringStart = 0
		d.ringLen = d.tailQuota
		return
	}
	for _, b := range p {
		if d.ringLen == d.tailQuota {
			// Drop oldest.
			oldOut := d.ringOut[d.ringStart]
			if oldOut {
				d.stdoutElide = true
			} else {
				d.stderrElide = true
			}
			d.ringStart = (d.ringStart + 1) % d.tailQuota
			d.ringLen--
		}
		idx := (d.ringStart + d.ringLen) % d.tailQuota
		d.ring[idx] = b
		d.ringOut[idx] = out
		d.ringLen++
	}
}

func (d *dualCapture) streamTail(out bool) []byte {
	if d.ringLen == 0 {
		return nil
	}
	var tail []byte
	for i := 0; i < d.ringLen; i++ {
		idx := (d.ringStart + i) % d.tailQuota
		if d.ringOut[idx] == out {
			tail = append(tail, d.ring[idx])
		}
	}
	return tail
}

func (d *dualCapture) streamString(out bool) string {
	head := d.stdoutHead
	elide := d.stdoutElide
	if !out {
		head = d.stderrHead
		elide = d.stderrElide
	}
	tail := d.streamTail(out)
	return string(assembleHeadTail(head, tail, elide && len(tail) > 0))
}

func (d *dualCapture) StdoutString() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streamString(true)
}

func (d *dualCapture) StderrString() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streamString(false)
}

func (d *dualCapture) Combined() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streamString(true) + d.streamString(false)
}

func (d *dualCapture) Truncated() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.truncated
}

func (d *dualCapture) Retained() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.max <= 0 {
		return len(d.stdoutHead) + len(d.stderrHead)
	}
	return d.headUsed + d.ringLen
}

func (d *dualCapture) Written() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.written
}

// writeHeadTail appends p into head then sliding tail under quotas.
// Peak retained bytes stay ≤ headQuota+tailQuota even when p is huge.
func writeHeadTail(head, tail *[]byte, truncated *bool, headQuota, tailQuota int, p []byte) {
	for len(p) > 0 {
		if len(*head) < headQuota {
			take := headQuota - len(*head)
			if take > len(p) {
				take = len(p)
			}
			*head = append(*head, p[:take]...)
			p = p[take:]
			continue
		}
		*truncated = true
		if tailQuota <= 0 {
			return
		}
		if len(p) >= tailQuota {
			*tail = append((*tail)[:0], p[len(p)-tailQuota:]...)
			return
		}
		*tail = append(*tail, p...)
		if len(*tail) > tailQuota {
			*tail = append((*tail)[:0], (*tail)[len(*tail)-tailQuota:]...)
		}
		return
	}
}

func assembleHeadTail(head, tail []byte, elide bool) []byte {
	if !elide || len(tail) == 0 {
		if len(tail) == 0 {
			out := make([]byte, len(head))
			copy(out, head)
			return out
		}
		out := make([]byte, 0, len(head)+len(tail))
		out = append(out, head...)
		out = append(out, tail...)
		return out
	}
	marker := []byte(captureElisionMarker)
	out := make([]byte, 0, len(head)+len(marker)+len(tail))
	out = append(out, head...)
	out = append(out, marker...)
	out = append(out, tail...)
	return out
}
