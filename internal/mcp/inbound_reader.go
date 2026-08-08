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
}

func newBoundedSSEReader(reader io.ReadCloser) *boundedSSEReader {
	return &boundedSSEReader{ReadCloser: reader, lineEmpty: true}
}

func (r *boundedSSEReader) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	for index, value := range buffer[:n] {
		if r.messageBytes >= maxMCPInboundMessageBytes {
			return index, errMCPInboundMessageTooLarge
		}
		r.messageBytes++
		if value == '\n' {
			if r.lineEmpty {
				r.messageBytes = 0
			}
			r.lineEmpty = true
			continue
		}
		if value != '\r' {
			r.lineEmpty = false
		}
	}
	return n, err
}
