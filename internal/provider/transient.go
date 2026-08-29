package provider

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// wsaECONNREFUSED is the Winsock error number for a refused connection
// (WSAECONNREFUSED). On Windows syscall.ECONNREFUSED is an invented
// APPLICATION_ERROR constant, not the Winsock value, and syscall.Errno.Is
// there has no refusal mapping, so errors.Is never matches a real refused
// dial on that platform. Any Windows code path must compare the unwrapped
// errno against this number; keep the comparison GOOS-gated, because 10061
// is a different errno on Unix.
const wsaECONNREFUSED = 10061

// IsConnectionRefused reports whether err is a connection-refused dial
// failure on this platform: errors.Is(err, syscall.ECONNREFUSED) covers
// Unix, and on Windows the unwrapped errno is compared against
// WSAECONNREFUSED. Match on errno values rather than message text:
// Winsock wording ("connectex: ... actively refused it") differs from the
// Unix phrase and is locale-dependent.
func IsConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return int(errno) == wsaECONNREFUSED
		}
	}
	return false
}

// TransientError marks a failure where the call never delivered an answer.
//
// The distinction matters to every caller that decides what a failure means.
// A model that answers badly is a result: the caller must judge it. A call
// that is cut by the network is not a result at all, and the caller has
// nothing to judge. Before this type, both looked the same to a workflow step,
// so one network fault ended a run that had hours of finished work.
//
// The provider layer owns this judgement because only it knows what its
// transport does. A caller asks IsTransient and stays free of vendor detail.
type TransientError struct{ Err error }

func (e *TransientError) Error() string {
	if e.Err == nil {
		return "transient provider failure"
	}
	return e.Err.Error()
}

func (e *TransientError) Unwrap() error { return e.Err }

// permanentError is the typed counterpart of TransientError: it pins a failure
// as permanent so no text phrase can flip it back to transient. The provider
// layer marks a refusal it knows holds (z.ai quota/plan 429 codes, for example)
// so a caller's IsTransient cannot re-run a whole step on a permanent block.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

// markPermanent wraps err so IsTransient reports false for it at every layer,
// whatever its text says. A nil error stays nil.
func markPermanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsTransient reports whether err says the call never delivered an answer.
//
// It reports true for a failure already marked TransientError, and for the
// transport faults every HTTP client can raise: a timeout, a reset or refused
// connection, a body that ends early, and a stream the peer tore down.
//
// A cancelled context is NOT transient. The caller stopped the call on
// purpose, so repeating it would work against that decision.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// A permanent marker wins over every other signal, including the
	// TransientError type test below: an error cannot be both marked, and a
	// provider refusal the transport already classified as permanent must not
	// re-run a whole step because its text happens to name a transient status.
	var perm *permanentError
	if errors.As(err, &perm) {
		return false
	}
	// A provider-marked transient wins over the bare-context rule below. The
	// provider layer marks a deadline it knows was NOT its own request timeout
	// (a tighter bound - the transport backstop or a parent step/run deadline -
	// cut the call while the answer was still on the wire) as TransientError at
	// the read site. Such a call never delivered an answer, so the step may
	// retry it under a fresh context; that marker must not be defeated by the
	// blanket rule that a bare context deadline is not transient.
	var transient *TransientError
	if errors.As(err, &transient) {
		return true
	}
	// A stream/read that went idle past its configured bound never delivered
	// an answer either - the connection stalled, it was not told to stop. See
	// idle_watchdog.go: distinct from context.DeadlineExceeded on purpose, so
	// this check must run independently of (and before) the context-deadline
	// test below, which deliberately reports false.
	if errors.Is(err, ErrStreamIdle) {
		return true
	}
	// A bound the TRANSPORT imposed on one phase of the exchange is a
	// transport fault, not a spent budget: the call never delivered an answer
	// and a fresh one can clear it. It must be tested before the context rule
	// below, because net/http's stage timeouts report themselves equal to
	// context.DeadlineExceeded - see IsTransportStageTimeout.
	if IsTransportStageTimeout(err) {
		return true
	}
	// A cancelled or expired context is NOT transient. The caller stopped the
	// call, or its deadline ran out; repeating it works against that decision
	// and, for an expired deadline, fails again at once.
	//
	// This test must come before the net.Error test below.
	// context.DeadlineExceeded satisfies net.Error with Timeout() == true, so
	// the generic timeout test would otherwise classify every expired step and
	// every expired run as a transport fault, and retry each one under the
	// context that just expired.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// No blanket test for a JSON syntax error or a bare EOF here. Those say
	// "these bytes are not the answer I expected", and at a call site that
	// parses an AGENT'S OUTPUT they describe a bad answer, not a broken
	// connection. Retrying a bad answer repeats it.
	//
	// The provider layer already wraps its OWN cut bodies and torn streams as
	// TransientError at the point of the read, where the difference is known,
	// so the type test above still covers every real transport fault.
	if IsConnectionRefused(err) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// A bare context.DeadlineExceeded is NOT transient here. A step deadline
	// and a run deadline both surface as that error, and repeating a call
	// under an expired context fails at once, every time. The provider layer
	// marks the complementary case at the read site: a deadline that fired
	// from a tighter bound than its own request timeout (transport backstop
	// or parent deadline) becomes TransientError there, so the type test above
	// still covers every real cut answer.
	return isTransientMessage(err)
}

// transientMessages name transport faults that arrive as plain text because
// the standard library and the HTTP/2 stack do not export a type for them.
//
// Matching text is weaker than matching a type, so the list stays short and
// holds only phrases that name a transport fault and nothing else. Each phrase
// describes how a connection died, never what a model said, so a model's own
// words cannot match by accident.
var transientMessages = []string{
	"stream error",            // HTTP/2 stream torn down mid-body
	"connection reset",        // peer dropped the connection
	"broken pipe",             // write to a closed connection
	"unexpected eof",          // body ended early
	"server closed idle conn", // pooled connection died between calls
	"no such host",            // transient DNS failure
	"i/o timeout",
	"tls handshake timeout",
	// Provider overload. The transport layer retries these first; they reach
	// a caller only after that budget is spent, and a fresh call later can
	// still succeed.
	"http 429",
	"http 408",
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"temporarily overloaded",
	"rate limited",
	"overloaded",
	"service unavailable",
}

func isTransientMessage(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, phrase := range transientMessages {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// markTransientReadDeadline marks a response-read deadline transient when the
// bound that fired was NOT the provider's own request timeout.
//
// A context.DeadlineExceeded during a read can come from three timers: the
// provider's own request context (req.Timeout), the transport backstop
// (http.Client.Timeout), or a parent step/run deadline. Only the first is a
// permanent statement about the call - the provider had its full budget and
// answered nothing, so retrying the same turn is a storm risk. The other two
// cut a call whose answer was still on the wire: the transport stalled (a
// fresh call can clear it), or a parent deadline fired (marking transient is
// still safe: the step retry loop re-checks its own context and fails fast
// under an expired parent deadline). Non-deadline errors pass through
// unchanged.
func markTransientReadDeadline(ctx context.Context, reqTimeout time.Duration, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && reqTimeout > 0 {
		deadline, ok := ctx.Deadline()
		if ok && time.Until(deadline) > 0 {
			return &TransientError{Err: err}
		}
	}
	return err
}

// asTransient wraps err as transient when the transport says the call never
// delivered an answer. It returns err unchanged otherwise, so a real answer
// and a permanent refusal keep their own meaning.
func asTransient(err error) error {
	if err == nil {
		return nil
	}
	var alreadyMarked *TransientError
	if errors.As(err, &alreadyMarked) {
		return err
	}
	if IsTransient(err) {
		return &TransientError{Err: err}
	}
	return err
}
