package mcp

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// oneByteReader caps every Read at one byte so a boundedSSEReader's per-event
// budget and blank-line reset are exercised across Read boundaries.
type oneByteReader struct{ reader io.Reader }

func (r oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return r.reader.Read(buffer)
	}
	return r.reader.Read(buffer[:1])
}

// TestSSEReaderAllowsEventDataAtExactLimit pins that an event whose data is
// exactly maxMCPInboundMessageBytes (plus its \n\n terminator) is accepted.
// The pre-fix reader counted the terminator toward the budget and rejected
// the event at the first newline after the count reached the limit.
func TestSSEReaderAllowsEventDataAtExactLimit(t *testing.T) {
	event := strings.Repeat("x", maxMCPInboundMessageBytes) + "\n\n"
	reader := newBoundedSSEReader(io.NopCloser(strings.NewReader(event)))
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read SSE event at the exact limit: %v", err)
	}
	if len(body) != len(event) {
		t.Fatalf("SSE stream len=%d, want %d", len(body), len(event))
	}
}

// TestSSEReaderIgnoresFramingInLimit pins that line-ending framing is never
// counted toward the per-event budget: a multi-line event whose data totals
// exactly the limit must pass even though data+framing exceeds it.
func TestSSEReaderIgnoresFramingInLimit(t *testing.T) {
	third := maxMCPInboundMessageBytes / 3
	last := maxMCPInboundMessageBytes - 2*third
	event := strings.Repeat("x", third) + "\n" + strings.Repeat("x", third) + "\r\n" + strings.Repeat("y", last) + "\n\n"
	if len(event) <= maxMCPInboundMessageBytes {
		t.Fatal("test event must exceed the limit once framing is included")
	}
	reader := newBoundedSSEReader(io.NopCloser(strings.NewReader(event)))
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read multi-line SSE event at the exact limit: %v", err)
	}
	if len(body) != len(event) {
		t.Fatalf("SSE stream len=%d, want %d", len(body), len(event))
	}
}

// TestSSEReaderRejectsEventDataOverLimit is the fail-closed negative path:
// data of max+1 bytes is rejected, exactly the limit of bytes is consumed
// before the rejection, and the over-limit state sticks on the next read
// (max+2 leaves one byte after the rejection to prove it).
func TestSSEReaderRejectsEventDataOverLimit(t *testing.T) {
	stream := strings.Repeat("x", maxMCPInboundMessageBytes+2)
	reader := newBoundedSSEReader(io.NopCloser(strings.NewReader(stream)))
	body, err := io.ReadAll(reader)
	if !errors.Is(err, errMCPInboundMessageTooLarge) {
		t.Fatalf("read over-limit SSE event err=%v, want errMCPInboundMessageTooLarge", err)
	}
	if len(body) != maxMCPInboundMessageBytes {
		t.Fatalf("consumed %d bytes before rejection, want %d", len(body), maxMCPInboundMessageBytes)
	}
	if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, errMCPInboundMessageTooLarge) {
		t.Fatalf("read after rejection err=%v, want the over-limit error to stick", err)
	}
}

// TestSSEReaderChunkedBoundariesEnforceLimitAndReset feeds the same streams
// through 1-byte reads to pin cross-Read limit enforcement and blank-line
// reset: messageBytes lives on the reader, so the budget must hold and reset
// across Read boundaries, never per call.
func TestSSEReaderChunkedBoundariesEnforceLimitAndReset(t *testing.T) {
	third := maxMCPInboundMessageBytes / 3
	last := maxMCPInboundMessageBytes - 2*third
	multiLine := strings.Repeat("x", third) + "\n" + strings.Repeat("x", third) + "\r\n" + strings.Repeat("y", last) + "\n\n"
	cases := []struct {
		name    string
		stream  string
		wantErr bool
	}{
		{name: "exact-limit", stream: strings.Repeat("x", maxMCPInboundMessageBytes) + "\n\n"},
		{name: "multi-line-framing", stream: multiLine},
		{name: "crlf", stream: strings.Repeat("x", maxMCPInboundMessageBytes-4) + "\r\n\r\n"},
		{name: "over-limit", stream: strings.Repeat("x", maxMCPInboundMessageBytes+2), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := newBoundedSSEReader(io.NopCloser(oneByteReader{reader: strings.NewReader(tc.stream)}))
			body, err := io.ReadAll(reader)
			if tc.wantErr {
				if !errors.Is(err, errMCPInboundMessageTooLarge) {
					t.Fatalf("read over-limit SSE stream err=%v, want errMCPInboundMessageTooLarge", err)
				}
				if len(body) != maxMCPInboundMessageBytes {
					t.Fatalf("consumed %d bytes before rejection, want %d", len(body), maxMCPInboundMessageBytes)
				}
				if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, errMCPInboundMessageTooLarge) {
					t.Fatalf("read after rejection err=%v, want the over-limit error to stick", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("read SSE stream through 1-byte reads: %v", err)
			}
			if len(body) != len(tc.stream) {
				t.Fatalf("SSE stream len=%d, want %d", len(body), len(tc.stream))
			}
		})
	}
}

// FuzzBoundedMCPReaders checks the bounded readers against a parsed model of
// their input. For the SSE reader the model asserts that framing is never
// counted: an over-limit error must fire only at a data byte that pushes the
// current event's data past the limit, never at a line ending or the
// blank-line terminator, and no single event may ever accept more than the
// limit of data bytes (fail-closed, no unbounded growth). The whole-body and
// per-line readers keep the shared invariants: no panic, a returned count
// within the buffer, and no (0, nil) read while input remains. The seed
// corpus pins the exact-limit, over-limit, multi-line framing, CRLF, and
// empty-stream cases.
func FuzzBoundedMCPReaders(f *testing.F) {
	third := maxMCPInboundMessageBytes / 3
	last := maxMCPInboundMessageBytes - 2*third
	seeds := []string{
		"",
		strings.Repeat("x", maxMCPInboundMessageBytes) + "\n\n",
		strings.Repeat("x", maxMCPInboundMessageBytes+1),
		strings.Repeat("x", third) + "\n" + strings.Repeat("x", third) + "\n" + strings.Repeat("y", last) + "\n\n",
		strings.Repeat("x", third) + "\r\n" + strings.Repeat("x", third) + "\r\n" + strings.Repeat("y", last) + "\r\n\r\n",
		"data: hello\ndata: world\n\n",
		"unterminated event with no blank line",
		strings.Repeat("s", 64) + "\n\n" + strings.Repeat("z", maxMCPInboundMessageBytes+1),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		checkSSEReaderInvariants(t, input)
		checkInboundReaderInvariants(t, input)
		checkStdioReaderInvariants(t, input)
	})
}

func checkSSEReaderInvariants(t *testing.T, input string) {
	t.Helper()
	reader := newBoundedSSEReader(io.NopCloser(strings.NewReader(input)))
	pos := 0       // input position of the next unread byte
	eventData := 0 // data bytes accepted in the current event
	lineEmpty := true
	buffer := make([]byte, 128)
	for {
		n, err := reader.Read(buffer)
		if n < 0 || n > len(buffer) {
			t.Fatalf("SSE read returned n=%d outside [0,%d]", n, len(buffer))
		}
		for _, value := range buffer[:n] {
			switch value {
			case '\n':
				if lineEmpty {
					eventData = 0
				}
				lineEmpty = true
			case '\r':
				// SSE framing: counted toward nothing.
			default:
				eventData++
				lineEmpty = false
				if eventData > maxMCPInboundMessageBytes {
					t.Fatalf("SSE reader accepted more than the limit of data bytes in one event")
				}
			}
			pos++
		}
		if err == errMCPInboundMessageTooLarge {
			if pos >= len(input) {
				t.Fatalf("SSE over-limit error with no rejected byte")
			}
			rejected := input[pos]
			if rejected == '\n' || rejected == '\r' {
				t.Fatalf("SSE over-limit error reported at framing byte %q", rejected)
			}
			if eventData < maxMCPInboundMessageBytes {
				t.Fatalf("SSE over-limit error before the event's data reached the limit: accepted %d data bytes", eventData)
			}
			return
		}
		if err != nil {
			return
		}
		if n == 0 {
			t.Fatalf("SSE read returned (0, nil) while input remains")
		}
	}
}

func checkInboundReaderInvariants(t *testing.T, input string) {
	t.Helper()
	reader := newBoundedInboundReader(io.NopCloser(strings.NewReader(input)))
	consumed := 0
	buffer := make([]byte, 128)
	for {
		n, err := reader.Read(buffer)
		if n < 0 || n > len(buffer) {
			t.Fatalf("inbound read returned n=%d outside [0,%d]", n, len(buffer))
		}
		consumed += n
		if err == errMCPInboundMessageTooLarge {
			if len(input) <= maxMCPInboundMessageBytes {
				t.Fatalf("inbound reader rejected a body of %d bytes at or below the limit", len(input))
			}
			return
		}
		if err != nil {
			return
		}
		if n == 0 {
			t.Fatalf("inbound read returned (0, nil) while input remains")
		}
		if consumed >= len(input) {
			if len(input) > maxMCPInboundMessageBytes {
				t.Fatalf("inbound reader accepted a body larger than the limit")
			}
			return
		}
	}
}

func checkStdioReaderInvariants(t *testing.T, input string) {
	t.Helper()
	reader := newBoundedStdioReader(io.NopCloser(strings.NewReader(input)))
	pos := 0
	lineBytes := 0
	buffer := make([]byte, 128)
	for {
		n, err := reader.Read(buffer)
		if n < 0 || n > len(buffer) {
			t.Fatalf("stdio read returned n=%d outside [0,%d]", n, len(buffer))
		}
		for _, value := range buffer[:n] {
			if value == '\n' {
				lineBytes = 0
			} else {
				lineBytes++
				if lineBytes > maxMCPInboundMessageBytes {
					t.Fatalf("stdio reader accepted more than the limit of bytes in one line")
				}
			}
			pos++
		}
		if err == errMCPInboundMessageTooLarge {
			if pos >= len(input) {
				t.Fatalf("stdio over-limit error with no rejected byte")
			}
			if input[pos] == '\n' {
				t.Fatalf("stdio over-limit error reported at the line terminator")
			}
			return
		}
		if err != nil {
			return
		}
		if n == 0 {
			t.Fatalf("stdio read returned (0, nil) while input remains")
		}
	}
}
