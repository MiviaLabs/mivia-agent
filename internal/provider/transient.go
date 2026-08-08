package provider

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

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
	var transient *TransientError
	if errors.As(err, &transient) {
		return true
	}
	// No blanket test for a JSON syntax error or a bare EOF here. Those say
	// "these bytes are not the answer I expected", and at a call site that
	// parses an AGENT'S OUTPUT they describe a bad answer, not a broken
	// connection. Retrying a bad answer repeats it.
	//
	// The provider layer already wraps its OWN cut bodies and torn streams as
	// TransientError at the point of the read, where the difference is known,
	// so the type test above still covers every real transport fault.
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// context.DeadlineExceeded is NOT transient here. A step deadline and a
	// run deadline both surface as that error, and repeating a call under an
	// expired context fails at once, every time. The provider layer knows
	// when the deadline was its OWN request timeout and marks those calls
	// TransientError explicitly, so the useful case is still covered.
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
