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

// boundedStdioReader enforces the per-message inbound budget for stdio MCP
// servers. The SDK's IOTransport decodes one JSON value per newline-delimited
// message and tolerates embedded newlines as JSON whitespace, so a single
// message can span many lines. depth/inString/escaped track the JSON
// structure of the in-flight value so a newline resets the budget only at a
// true message terminator (structural depth 0, outside any string), never
// inside the value. Every byte of a message - embedded newlines included -
// counts toward maxMCPInboundMessageBytes, so the SDK's json.Decoder can
// never buffer an unbounded single value.
type boundedStdioReader struct {
	io.ReadCloser
	messageBytes int
	depth        int
	inString     bool
	escaped      bool
}

func newBoundedStdioReader(reader io.ReadCloser) *boundedStdioReader {
	return &boundedStdioReader{ReadCloser: reader}
}

func (r *boundedStdioReader) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	for index, value := range buffer[:n] {
		// A newline at structural depth 0 outside any string terminates the
		// message: it is never counted toward the budget and never rejected,
		// preserving the exact-limit-plus-terminator pass. A newline inside a
		// JSON value (between tokens or inside a string) is data.
		if value == '\n' && r.depth == 0 && !r.inString {
			r.messageBytes = 0
			continue
		}
		if r.messageBytes >= maxMCPInboundMessageBytes {
			return index, errMCPInboundMessageTooLarge
		}
		r.messageBytes++
		r.updateJSONState(value)
	}
	return n, err
}

// updateJSONState tracks the JSON structural depth and string/escape state of
// the in-flight message. A backslash inside a string escapes the next byte, so
// an escaped quote or backslash cannot close the string or a structural brace
// inside a string cannot change the depth. Depth is clamped at 0 so malformed
// input can only make the budget more conservative, never less.
func (r *boundedStdioReader) updateJSONState(value byte) {
	if r.inString {
		if r.escaped {
			r.escaped = false
			return
		}
		switch value {
		case '\\':
			r.escaped = true
		case '"':
			r.inString = false
		}
		return
	}
	switch value {
	case '"':
		r.inString = true
	case '{', '[':
		r.depth++
	case '}', ']':
		if r.depth > 0 {
			r.depth--
		}
	}
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
