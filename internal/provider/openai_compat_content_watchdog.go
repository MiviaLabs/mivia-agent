package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStreamContentIdleTimeout bounds the gap between successive CONTENT
// chunks on an SSE stream: a stream that sends no chunk which would contribute
// to the turn's answer within this bound is stalled, whatever else it sends.
//
// This bound exists beside the byte-level watchdogs (idle_watchdog.go) because
// the two watch for different failures. A byte-idle watchdog fires on a silent
// connection, but a keepalive trickle feeds it forever: some providers emit
// SSE comments or blank lines every few seconds while the model answer never
// advances. Nested subagent turns run non-streaming at the call site, so no
// human sees the stall; only a content-level bound detects it in bounded time.
const DefaultStreamContentIdleTimeout = 90 * time.Second

// streamContentIdleTimeoutNs holds the active content-idle bound as
// nanoseconds, process-wide. The atomic keeps concurrent reads race-free
// against setStreamContentIdleTimeout, and mirrors streamIdleTimeoutNs in
// idle_watchdog.go. NewForProvider sets it from the resolved [provider]
// stream_content_idle_timeout_seconds via SetStreamWatchdogTimeouts. The
// bound stays independent of stream_idle_timeout: that knob governs plain
// byte-idle and must keep its own meaning.
var streamContentIdleTimeoutNs atomic.Int64

func init() {
	streamContentIdleTimeoutNs.Store(int64(DefaultStreamContentIdleTimeout))
}

// setStreamContentIdleTimeout overrides the process-wide content-idle bound.
// Config wiring goes through SetStreamWatchdogTimeouts (idle_watchdog.go),
// which delegates here from NewForProvider; tests also shrink the bound
// directly. A non-positive value is ignored, so a caller that knows no bound
// never resets it to zero.
func setStreamContentIdleTimeout(d time.Duration) {
	if d > 0 {
		streamContentIdleTimeoutNs.Store(int64(d))
	}
}

func streamContentIdleTimeout() time.Duration {
	return time.Duration(streamContentIdleTimeoutNs.Load())
}

// sseContentWatchdogReader wraps an SSE stream body and fails fast when the
// stream stops making content progress. It sits between wrapWithIdleWatchdog
// (the byte-level watchdog) and the downstream scanner on the stream-transport
// turn path: the byte-level watchdog cannot see a keepalive trickle, this one
// can.
//
// MECHANICS. A pump goroutine reads the source, splits the bytes into SSE
// lines, classifies each line against the progress predicate, and forwards the
// bytes UNMODIFIED over resultCh. The send is blocking on the data path, so a
// slow downstream applies backpressure and no byte is lost; the only other way
// the send completes is a closed done channel, which is the abort exit. Read
// arms a resettable deadline: only a line the progress predicate accepts
// pushes the deadline out. When the deadline fires, Read closes done (once)
// and returns an error that wraps ErrStreamIdle, so the existing transient
// classification and logging work unchanged.
//
// PUMP-EXIT GUARANTEE. On the abort path the guarantee holds: done is closed,
// so the pump's next send returns at once, and the caller closes the response
// body, which unblocks an in-flight source read. The context-cancel path keeps
// the same bounded leak the mirrored idleWatchdogReader carries today: a pump
// blocked on a full resultCh exits only when the body close makes the source
// return. Callers that want a hard release call Close.
//
// BYTES ARE NEVER REWRITTEN. The reader classifies incrementally and forwards
// exact wire bytes, so the downstream scanner sees the stream byte for byte.
type sseContentWatchdogReader struct {
	src      io.Reader
	label    string
	resultCh chan wdContentResult
	// done closes when the watchdog aborts or Close runs. closeOnce guards
	// the close: the abort branch and Close can both run, and a double close
	// panics.
	done      chan struct{}
	closeOnce sync.Once
	startOnce sync.Once

	// progressDeadline is the instant the watchdog aborts if no progress
	// line arrives first. Read owns it; the scanner calls Read sequentially.
	progressDeadline time.Time
	leftover         []byte
	pendingErr       error
	// sawData records whether any data: line ever arrived. The
	// stream-transport path reads it after a stall: an abort with zero data
	// lines says the provider never streamed at all.
	sawData atomic.Bool
}

// wdContentResult is one forwarded byte segment, with the classification the
// pump gave it. A segment with progress set pushes the content deadline out.
type wdContentResult struct {
	buf      []byte
	progress bool
	err      error
}

func newSSEContentWatchdogReader(src io.Reader, label string) *sseContentWatchdogReader {
	return &sseContentWatchdogReader{
		src:      src,
		label:    label,
		resultCh: make(chan wdContentResult, 1),
		done:     make(chan struct{}),
	}
}

// Close releases the pump after a non-abort exit. Safe to call more than
// once, and safe to call after an abort: closeOnce makes the second close a
// no-op.
func (r *sseContentWatchdogReader) Close() { r.closeDone() }

func (r *sseContentWatchdogReader) closeDone() {
	r.closeOnce.Do(func() { close(r.done) })
}

// pump reads the source and forwards classified segments. It runs for the
// reader's lifetime once started. On a source error it first classifies and
// forwards any partial trailing line - a text-only stream can end with a
// final data line and no newline, and dropping that tail would truncate the
// answer - then forwards the error itself.
func (r *sseContentWatchdogReader) pump() {
	clf := sseContentClassifier{}
	buf := make([]byte, 32*1024)
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			for _, seg := range clf.classify(buf[:n]) {
				if seg.dataLine {
					r.sawData.Store(true)
				}
				select {
				case r.resultCh <- wdContentResult{buf: seg.data, progress: seg.progress}:
				case <-r.done:
					return
				}
			}
		}
		if err != nil {
			for _, seg := range clf.finish() {
				if seg.dataLine {
					r.sawData.Store(true)
				}
				select {
				case r.resultCh <- wdContentResult{buf: seg.data, progress: seg.progress}:
				case <-r.done:
					return
				}
			}
			select {
			case r.resultCh <- wdContentResult{err: err}:
			case <-r.done:
			}
			return
		}
	}
}

// sawDataLine reports whether any data: line arrived before the call. It is
// safe to call while the pump runs.
func (r *sseContentWatchdogReader) sawDataLine() bool { return r.sawData.Load() }

// Read implements io.Reader. Not safe for concurrent calls - matching every
// io.Reader in this codebase, and the body it wraps.
func (r *sseContentWatchdogReader) Read(p []byte) (int, error) {
	if n := r.drainLeftover(p); n > 0 {
		return n, nil
	}
	if r.pendingErr != nil {
		err := r.pendingErr
		r.pendingErr = nil
		return 0, err
	}
	r.startOnce.Do(func() {
		r.progressDeadline = time.Now().Add(streamContentIdleTimeout())
		go r.pump()
	})
	remaining := time.Until(r.progressDeadline)
	if remaining <= 0 {
		return r.abort()
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case out := <-r.resultCh:
		if out.progress {
			r.progressDeadline = time.Now().Add(streamContentIdleTimeout())
		}
		return r.deliver(p, out)
	case <-timer.C:
		return r.abort()
	}
}

// abort stops the watchdog and returns the stall error. The error wraps
// ErrStreamIdle so transient.go and the stall-retry gate see the same class
// the byte-level watchdog reports.
func (r *sseContentWatchdogReader) abort() (int, error) {
	r.closeDone()
	err := fmt.Errorf("%s: %w (no content chunk within content-idle bound %s)", r.label, ErrStreamIdle, streamContentIdleTimeout())
	r.pendingErr = err
	return 0, err
}

func (r *sseContentWatchdogReader) drainLeftover(p []byte) int {
	if len(r.leftover) == 0 {
		return 0
	}
	n := copy(p, r.leftover)
	r.leftover = r.leftover[n:]
	return n
}

// deliver copies a pump result into the caller's buffer, stashing anything
// that does not fit as leftover and any accompanying error as pendingErr so
// both survive in order for later Read calls.
func (r *sseContentWatchdogReader) deliver(p []byte, out wdContentResult) (int, error) {
	if len(out.buf) == 0 {
		return 0, out.err
	}
	n := copy(p, out.buf)
	if n < len(out.buf) {
		r.leftover = out.buf[n:]
	}
	if out.err != nil {
		r.pendingErr = out.err
	}
	return n, nil
}

// sseContentLineLimit caps one assembled SSE line. It matches the 1 MiB
// scanner cap in readTurnStream (openai_compat_stream.go). A line past the
// cap FAILS OPEN: the accumulated bytes go downstream unmodified and count as
// progress, and the rest of the line streams through as raw progress segments
// until a newline arrives. The watchdog never aborts on size and never
// re-serializes, so downstream behavior stays exactly what it was.
const sseContentLineLimit = 1024 * 1024

// sseSegment is one byte span the classifier hands to the pump, with the
// verdicts for the SSE line it belongs to: progress drives the deadline, and
// dataLine says the line carried a data field at all.
type sseSegment struct {
	data     []byte
	progress bool
	dataLine bool
}

// sseContentClassifier assembles SSE lines from a byte stream and classifies
// each complete line. It lives in the pump goroutine alone, so it needs no
// locks.
type sseContentClassifier struct {
	line     []byte
	oversize bool
	// oversizeData carries the data-line verdict of the line currently
	// streaming in oversize mode.
	oversizeData bool
}

// classify consumes one source read and returns the segments it completes.
// Segment boundaries follow SSE line ends, but the bytes in each segment are
// the exact wire bytes, newline included, so the concatenation of all
// segments is byte-identical to the input.
func (c *sseContentClassifier) classify(chunk []byte) []sseSegment {
	var segs []sseSegment
	for len(chunk) > 0 {
		if c.oversize {
			idx := bytes.IndexByte(chunk, '\n')
			if idx < 0 {
				segs = append(segs, sseSegment{data: append([]byte(nil), chunk...), progress: true, dataLine: c.oversizeData})
				chunk = nil
				continue
			}
			segs = append(segs, sseSegment{data: append([]byte(nil), chunk[:idx+1]...), progress: true, dataLine: c.oversizeData})
			chunk = chunk[idx+1:]
			c.oversize = false
			continue
		}
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			c.line = append(c.line, chunk...)
			chunk = nil
			if len(c.line) > sseContentLineLimit {
				data := sseLineHasDataPrefix(c.line)
				segs = append(segs, sseSegment{data: c.line, progress: true, dataLine: data})
				c.line = nil
				c.oversize = true
				c.oversizeData = data
			}
			continue
		}
		c.line = append(c.line, chunk[:idx+1]...)
		chunk = chunk[idx+1:]
		line := c.line
		c.line = nil
		segs = append(segs, sseSegment{data: line, progress: sseLineIsProgress(line), dataLine: sseLineHasDataPrefix(line)})
	}
	return segs
}

// finish flushes a partial trailing line at end of stream and classifies it
// like a complete line. The verdict does not gate delivery here - the bytes
// go downstream either way - but a final data line that carries content
// counts as progress, which keeps the deadline honest to the last byte.
func (c *sseContentClassifier) finish() []sseSegment {
	if c.oversize {
		c.oversize = false
		return nil
	}
	if len(c.line) == 0 {
		return nil
	}
	line := c.line
	c.line = nil
	return []sseSegment{{data: line, progress: sseLineIsProgress(line), dataLine: sseLineHasDataPrefix(line)}}
}

// sseLineHasDataPrefix reports whether a line is a data field line, whatever
// its payload. It is the literal "a data: line arrived" test, so it counts
// role-only chunks and [DONE] too.
func sseLineHasDataPrefix(line []byte) bool {
	text := bytes.TrimSpace(line)
	return bytes.HasPrefix(text, []byte("data:"))
}

// sseLineIsProgress is the structural progress predicate: it reports whether
// one complete SSE line would contribute to any accumulator readTurnStream
// keeps - content, reasoning in any decoded shape (reasoning_content,
// reasoning, or reasoning_details entries with text or summary), web_search
// at top level or delta level, any tool_calls fragment - or set finish_reason
// or usage. SSE comments, blank lines, event:/id: lines, and a chunk that
// decodes to no accumulator contribution (a role-only first chunk, an empty
// delta) are keepalive, not progress.
func sseLineIsProgress(line []byte) bool {
	text := bytes.TrimSpace(line)
	if len(text) == 0 {
		return false
	}
	if text[0] == ':' {
		return false
	}
	if !bytes.HasPrefix(text, []byte("data:")) {
		return false
	}
	payload := bytes.TrimSpace(text[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return false
	}
	var chunk chatResponseBody
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return false
	}
	return chunkContributesProgress(&chunk)
}

// chunkContributesProgress applies the accumulator test to one decoded SSE
// chunk. It mirrors readTurnStream's accumulators field for field, including
// the R0-1 rule that a reasoning_details entry counts only when it carries
// text or summary.
func chunkContributesProgress(chunk *chatResponseBody) bool {
	if chunk.Usage != nil || len(chunk.WebSearch) > 0 {
		return true
	}
	if len(chunk.Choices) == 0 {
		return false
	}
	ch := chunk.Choices[0]
	if ch.FinishReason != "" {
		return true
	}
	d := ch.Delta
	if d.Content != "" || d.ReasoningContent != "" || d.Reasoning != "" {
		return true
	}
	for _, det := range d.ReasoningDetails {
		if det.Text != "" || det.Summary != "" {
			return true
		}
	}
	if len(d.ToolCalls) > 0 || len(d.WebSearch) > 0 {
		return true
	}
	return false
}
