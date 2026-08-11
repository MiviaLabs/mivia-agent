package mcp

import (
	"errors"
	"io"
)

const maxMCPInboundMessageBytes = 8 << 20

var errMCPInboundMessageTooLarge = errors.New("MCP inbound message exceeds limit")

type boundedInboundReader struct {
	io.ReadCloser
	remaining int
}

func newBoundedInboundReader(reader io.ReadCloser) *boundedInboundReader {
	return &boundedInboundReader{ReadCloser: reader, remaining: maxMCPInboundMessageBytes}
}

func (r *boundedInboundReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, errMCPInboundMessageTooLarge
		}
		return 0, err
	}
	if len(buffer) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.ReadCloser.Read(buffer)
	r.remaining -= n
	return n, err
}

type boundedStdioReader struct {
	io.ReadCloser
	messageBytes int
}

func newBoundedStdioReader(reader io.ReadCloser) *boundedStdioReader {
	return &boundedStdioReader{ReadCloser: reader}
}

func (r *boundedStdioReader) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	for index, value := range buffer[:n] {
		if value == '\n' {
			r.messageBytes = 0
			continue
		}
		if r.messageBytes >= maxMCPInboundMessageBytes {
			return index, errMCPInboundMessageTooLarge
		}
		r.messageBytes++
	}
	return n, err
}

type boundedSSEReader struct {
	io.ReadCloser
	messageBytes int
	lineEmpty    bool
	// stickyErr holds errMCPInboundMessageTooLarge after a rejection. All
	// later reads return it. Without it, the error vanishes when the
	// rejection consumes the final input byte and the next read sees EOF.
	stickyErr error
}

func newBoundedSSEReader(reader io.ReadCloser) *boundedSSEReader {
	return &boundedSSEReader{ReadCloser: reader, lineEmpty: true}
}

func (r *boundedSSEReader) Read(buffer []byte) (int, error) {
	if r.stickyErr != nil {
		return 0, r.stickyErr
	}
	n, err := r.ReadCloser.Read(buffer)
	for index, value := range buffer[:n] {
		// SSE framing - line endings and the blank-line event terminator -
		// is never counted toward the per-event budget. The budget covers
		// only the event's data bytes, and the over-limit check runs before
		// the blank-line reset so an event whose data is exactly at the
		// limit (plus its \n\n terminator) is accepted while max+1 still
		// fails closed.
		if value == '\n' {
			if r.lineEmpty {
				r.messageBytes = 0
			}
			r.lineEmpty = true
			continue
		}
		if value == '\r' {
			continue
		}
		if r.messageBytes >= maxMCPInboundMessageBytes {
			r.stickyErr = errMCPInboundMessageTooLarge
			return index, r.stickyErr
		}
		r.messageBytes++
		r.lineEmpty = false
	}
	return n, err
}
