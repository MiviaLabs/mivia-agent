package provider

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// withStreamContentIdleTimeout shrinks the content-idle bound for one test
// and restores the previous value at cleanup. It mirrors withWatchdogTimeouts
// in idle_watchdog_test.go. Tests that touch the atomic bound must not run in
// parallel.
func withStreamContentIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := streamContentIdleTimeout()
	setStreamContentIdleTimeout(d)
	t.Cleanup(func() { setStreamContentIdleTimeout(prev) })
}

// keepaliveTrickleReader emits a keepalive line every tick and never sends a
// content chunk. It stands in for a provider connection whose transport
// dribbles bytes while the model answer never advances.
type keepaliveTrickleReader struct {
	tick time.Duration
	stop chan struct{}
	once sync.Once
}

func newKeepaliveTrickleReader(tick time.Duration) *keepaliveTrickleReader {
	return &keepaliveTrickleReader{tick: tick, stop: make(chan struct{})}
}

func (r *keepaliveTrickleReader) stopNow() { r.once.Do(func() { close(r.stop) }) }

func (r *keepaliveTrickleReader) Read(p []byte) (int, error) {
	select {
	case <-r.stop:
		return 0, io.EOF
	case <-time.After(r.tick):
		return copy(p, ": keepalive\n\n"), nil
	}
}

// TestSSEContentWatchdogKeepaliveTrickleAborts proves the reason this reader
// exists: a connection that dribbles keepalive bytes feeds the byte-level
// idle watchdog forever, but the content watchdog aborts inside its bound
// because no line ever contributes content.
func TestSSEContentWatchdogKeepaliveTrickleAborts(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	src := newKeepaliveTrickleReader(25 * time.Millisecond)
	t.Cleanup(src.stopNow)

	r := newSSEContentWatchdogReader(src, "test")
	t.Cleanup(r.Close)

	buf := make([]byte, 64)
	start := time.Now()
	var err error
	for err == nil {
		if _, err = r.Read(buf); err != nil {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("content watchdog did not abort within 5s")
		}
	}
	elapsed := time.Since(start)
	if !strings.Contains(err.Error(), "content-idle") {
		t.Fatalf("abort error should name the content-idle bound, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Read blocked %s past the 300ms content-idle bound - the watchdog did not fire", elapsed)
	}
	// The abort path and Close() both close done. The keepalive-abort run hits
	// both sites, so this line also proves the close is idempotent: a plain
	// close would panic here on the second close.
	r.Close()
}

// TestSSEContentWatchdogAbortWrapsErrStreamIdle locks the error class: the
// stall-retry gate and transient.go classify on ErrStreamIdle, so the content
// abort must satisfy errors.Is for it.
func TestSSEContentWatchdogAbortWrapsErrStreamIdle(t *testing.T) {
	withStreamContentIdleTimeout(t, 200*time.Millisecond)
	src := newKeepaliveTrickleReader(25 * time.Millisecond)
	t.Cleanup(src.stopNow)

	r := newSSEContentWatchdogReader(src, "test")
	t.Cleanup(r.Close)

	buf := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		_, err = r.Read(buf)
		if err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("watchdog never aborted")
	}
	if !IsTransient(err) {
		t.Fatalf("content-idle abort must classify as transient, got: %v", err)
	}
}

// scriptedByteReader feeds fixed byte spans in order, pausing delay when a
// span starts, then returns io.EOF. A span larger than the caller's buffer
// takes several Reads; no span byte is ever dropped.
type scriptedByteReader struct {
	spans [][]byte
	delay time.Duration
	i     int
	pos   int
}

func (r *scriptedByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.spans) {
		return 0, io.EOF
	}
	if r.pos == 0 && r.delay > 0 {
		time.Sleep(r.delay)
	}
	n := copy(p, r.spans[r.i][r.pos:])
	r.pos += n
	if r.pos >= len(r.spans[r.i]) {
		r.i++
		r.pos = 0
	}
	return n, nil
}

// TestSSEContentWatchdogContentTrickleSurvives proves the positive side: a
// stream whose chunks are slow but real content survives a bound the trickle
// would fail, and the forwarded bytes are exact.
func TestSSEContentWatchdogContentTrickleSurvives(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	chunk := []byte("data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	var spans [][]byte
	for i := 0; i < 8; i++ {
		spans = append(spans, chunk)
	}
	src := &scriptedByteReader{spans: spans, delay: 50 * time.Millisecond}
	r := newSSEContentWatchdogReader(src, "test")

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("content trickle past the byte-dribble pattern must complete, got: %v", err)
	}
	var want []byte
	for _, s := range spans {
		want = append(want, s...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("forwarded bytes differ from the wire bytes: got %d bytes, want %d", len(got), len(want))
	}
}

// TestSSEContentWatchdogForwardsPartialTailLine proves the EOF rule: a final
// data line with no trailing newline is classified and forwarded before the
// EOF propagates, so a text-only stream is not truncated at its last line.
func TestSSEContentWatchdogForwardsPartialTailLine(t *testing.T) {
	withStreamContentIdleTimeout(t, 2*time.Second)
	src := &scriptedByteReader{spans: [][]byte{
		[]byte("data: " + `{"choices":[{"delta":{"content":"head"}}]}` + "\n\n"),
		[]byte("data: " + `{"choices":[{"delta":{"content":"tail"}}]}`), // no newline, then EOF
	}}
	r := newSSEContentWatchdogReader(src, "test")

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := "data: " + `{"choices":[{"delta":{"content":"head"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"content":"tail"}}]}`
	if string(got) != want {
		t.Fatalf("tail line lost: got %q, want %q", got, want)
	}
}

// TestSSELineIsProgressTable locks the structural progress predicate line by
// line. Progress means the decoded chunk would contribute to any accumulator
// readTurnStream keeps; everything else is keepalive.
func TestSSELineIsProgressTable(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		progress bool
	}{
		{"content delta", "data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n", true},
		{"reasoning_content delta", "data: " + `{"choices":[{"delta":{"reasoning_content":"think"}}]}` + "\n", true},
		{"reasoning delta", "data: " + `{"choices":[{"delta":{"reasoning":"think"}}]}` + "\n", true},
		{"reasoning_details text entry", "data: " + `{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think"}]}}]}` + "\n", true},
		{"reasoning_details summary entry", "data: " + `{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"s"}]}}]}` + "\n", true},
		{"tool_calls fragment", "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}` + "\n", true},
		{"web_search delta-level", "data: " + `{"choices":[{"delta":{"web_search":[{"title":"t"}]}}]}` + "\n", true},
		{"web_search top-level", "data: " + `{"choices":[],"web_search":[{"title":"t"}]}` + "\n", true},
		{"finish_reason", "data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n", true},
		{"usage", "data: " + `{"choices":[],"usage":{"prompt_tokens":1}}` + "\n", true},
		{"sse comment", ": keepalive\n", false},
		{"blank line", "\n", false},
		{"event line", "event: message\n", false},
		{"id line", "id: 42\n", false},
		{"role-only first chunk", "data: " + `{"choices":[{"delta":{"role":"assistant"}}]}` + "\n", false},
		{"empty delta chunk", "data: " + `{"choices":[{"delta":{}}]}` + "\n", false},
		{"done sentinel", "data: [DONE]\n", false},
		{"malformed json", "data: {oops\n", false},
		{"payload-less reasoning_details entry", "data: " + `{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}` + "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sseLineIsProgress([]byte(tt.line)); got != tt.progress {
				t.Fatalf("sseLineIsProgress(%q) = %v, want %v", tt.line, got, tt.progress)
			}
		})
	}
}

// TestSSEContentWatchdogOversizeLineFailsOpen proves the oversize rule: a
// line past the 1 MiB cap is forwarded unmodified, counts as progress, and
// never trips the abort - the watchdog must not make a big line worse.
func TestSSEContentWatchdogOversizeLineFailsOpen(t *testing.T) {
	withStreamContentIdleTimeout(t, 400*time.Millisecond)
	big := bytes.Repeat([]byte("x"), sseContentLineLimit+512)
	src := &scriptedByteReader{spans: [][]byte{
		big[:64*1024],
		big[64*1024 : 128*1024],
		big[128*1024:],
		[]byte("\ndata: " + `{"choices":[{"delta":{"content":"done"}}]}` + "\n"),
	}, delay: 20 * time.Millisecond}
	r := newSSEContentWatchdogReader(src, "test")

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("oversize line must fail open, got: %v", err)
	}
	var want []byte
	want = append(want, big...)
	want = append(want, []byte("\ndata: "+`{"choices":[{"delta":{"content":"done"}}]}`+"\n")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("oversize bytes corrupted: got %d bytes, want %d", len(got), len(want))
	}
}
