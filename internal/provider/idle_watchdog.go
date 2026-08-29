package provider

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout bounds the gap between successive bytes on a
// provider read, once the first byte has arrived. DefaultStreamFirstByteTimeout
// bounds the longer wait for that first byte (headers already arrived, but
// the provider has not started answering yet - reasoning models can sit
// silent for a while before the first token).
//
// Both exist because, before this file, the ONLY bound on a provider read
// (streaming or not) was http.Client.Timeout (DefaultHTTPTimeout, 15
// minutes), covering connection + headers + the entire body with no
// per-chunk reset. A dead-but-open connection sat silent for up to that full
// window with no observable signal.
const (
	DefaultStreamIdleTimeout      = 100 * time.Second
	DefaultStreamFirstByteTimeout = 240 * time.Second
)

// ErrStreamIdle marks a provider read that received no bytes within the
// configured bound. It is distinct from context.DeadlineExceeded on purpose:
// a deadline reports an exhausted budget the caller chose, while
// ErrStreamIdle reports a connection that went silent - callers (transient.go,
// downstream retry logic) tell the two apart via errors.Is/errors.As.
var ErrStreamIdle = errors.New("provider: stream idle timeout")

// streamIdleTimeoutNs and streamFirstByteTimeoutNs hold the active watchdog
// bounds as nanoseconds, process-wide. mivia runs one active provider
// configuration per process, so a package-level knob set once at
// provider.NewForProvider is simpler than threading a new option through
// every OpenAI-compatible factory (openrouter, deepseek, zai, ollama,
// llmgateway, llmproxycli, minimax) for a value that is never per-provider in
// practice. atomic.Int64 keeps concurrent chat sessions' reads race-free
// against SetStreamWatchdogTimeouts.
var (
	streamIdleTimeoutNs      atomic.Int64
	streamFirstByteTimeoutNs atomic.Int64
)

func init() {
	streamIdleTimeoutNs.Store(int64(DefaultStreamIdleTimeout))
	streamFirstByteTimeoutNs.Store(int64(DefaultStreamFirstByteTimeout))
}

// SetStreamWatchdogTimeouts configures the process-wide idle and first-byte
// bounds every OpenAICompat client's stream and non-stream body reads honor.
// A non-positive value leaves the corresponding bound unchanged, so a caller
// that only knows one of the two never resets the other to zero.
func SetStreamWatchdogTimeouts(idle, firstByte time.Duration) {
	if idle > 0 {
		streamIdleTimeoutNs.Store(int64(idle))
	}
	if firstByte > 0 {
		streamFirstByteTimeoutNs.Store(int64(firstByte))
	}
}

func streamIdleTimeout() time.Duration {
	return time.Duration(streamIdleTimeoutNs.Load())
}

func streamFirstByteTimeout() time.Duration {
	return time.Duration(streamFirstByteTimeoutNs.Load())
}

// wrapWithIdleWatchdog wraps body so a read that receives no bytes within the
// configured first-byte/idle bounds fails fast instead of blocking up to the
// transport's absolute DefaultHTTPTimeout (15 minutes). Applied at every
// response-body read site this client owns: streaming (chatTurnStream,
// ChatStream) and non-streaming (doJSONOnce). The non-streaming site is the
// operationally common one - nested/subagent turns never stream
// (MultiStepHandler never sets FinalWriter) - so every subagent-context turn
// takes the non-streaming path this watchdog now covers.
func (c *OpenAICompat) wrapWithIdleWatchdog(body io.Reader) io.Reader {
	return wrapBodyWithIdleWatchdog(body, c.name)
}

// wrapBodyWithIdleWatchdog is the client-independent form of the same guard,
// so a client that is not OpenAICompat cannot quietly go without one. Every
// provider body read in this package goes through here: the native Anthropic
// client's non-stream and SSE reads, and the retry layer's connection-reuse
// drain, all of which previously read a possibly-dead socket with no bound
// but the 15-minute client wall.
func wrapBodyWithIdleWatchdog(body io.Reader, label string) io.Reader {
	return newIdleWatchdogReader(body, streamFirstByteTimeout(), streamIdleTimeout(), label)
}

// idleWatchdogReader wraps an io.Reader (a provider HTTP response body) so a
// silently dead connection is detected without a per-call context deadline.
//
// http.Response.Body exposes no per-Read timeout: SetReadDeadline lives only
// on the raw net.Conn, which http.Client does not hand back once dialing
// finishes, and bufio.Scanner / io.ReadAll call Read directly with no context
// parameter to thread a cancellation through. Racing each logical read
// against a resettable timer in a background goroutine is the standard
// workaround for exactly this stdlib gap, and needs no context plumbing
// through either caller.
type idleWatchdogReader struct {
	src           io.Reader
	firstByte     time.Duration
	idle          time.Duration
	label         string
	seenFirstByte bool
	leftover      []byte
	pendingErr    error

	startOnce sync.Once
	resultCh  chan wdReadResult
}

// wdReadResult is one completed src.Read, forwarded from pump to Read.
type wdReadResult struct {
	buf []byte
	err error
}

func newIdleWatchdogReader(src io.Reader, firstByte, idle time.Duration, label string) *idleWatchdogReader {
	return &idleWatchdogReader{
		src:       src,
		firstByte: firstByte,
		idle:      idle,
		label:     label,
		resultCh:  make(chan wdReadResult, 1),
	}
}

// pump issues sequential blocking Reads against src and forwards each result
// over resultCh. It runs for the reader's lifetime once started.
//
// If Read times out waiting for a result, pump keeps running: its next send
// blocks until something drains resultCh, which happens only if Read is
// called again. That is a bounded goroutine leak, but only for the lifetime
// of an idle-timed-out stream - every call site closes the response body
// immediately on error (chatTurnStream, ChatStream, doJSONOnce all run under
// `defer resp.Body.Close()`), which unblocks the in-flight src.Read and lets
// pump exit on its next send attempt.
func (r *idleWatchdogReader) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.src.Read(buf)
		out := wdReadResult{err: err}
		if n > 0 {
			out.buf = append([]byte(nil), buf[:n]...)
		}
		r.resultCh <- out
		if err != nil {
			return
		}
	}
}

// Read implements io.Reader. Not safe for concurrent calls - matching every
// io.Reader in this codebase, and the http.Response.Body it wraps.
func (r *idleWatchdogReader) Read(p []byte) (int, error) {
	if n := r.drainLeftover(p); n > 0 {
		return n, nil
	}
	if r.pendingErr != nil {
		err := r.pendingErr
		r.pendingErr = nil
		return 0, err
	}
	r.startOnce.Do(func() { go r.pump() })
	timeout := r.idle
	if !r.seenFirstByte {
		timeout = r.firstByte
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-r.resultCh:
		return r.deliver(p, out)
	case <-timer.C:
		return 0, fmt.Errorf("%s: %w (bound %s)", r.label, ErrStreamIdle, timeout)
	}
}

func (r *idleWatchdogReader) drainLeftover(p []byte) int {
	if len(r.leftover) == 0 {
		return 0
	}
	n := copy(p, r.leftover)
	r.leftover = r.leftover[n:]
	return n
}

// deliver copies a pump result into the caller's buffer, stashing anything
// that does not fit as leftover and any accompanying error as pendingErr so
// both survive to be returned on later Read calls in order.
func (r *idleWatchdogReader) deliver(p []byte, out wdReadResult) (int, error) {
	if len(out.buf) == 0 {
		return 0, out.err
	}
	r.seenFirstByte = true
	n := copy(p, out.buf)
	if n < len(out.buf) {
		r.leftover = out.buf[n:]
	}
	if out.err != nil {
		r.pendingErr = out.err
	}
	return n, nil
}
