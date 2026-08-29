package provider

import (
	"context"
	"errors"
)

// A transport STAGE timeout is a bound the HTTP machinery puts on one phase of
// an exchange - the wait for response headers (Transport.ResponseHeaderTimeout)
// or the client-wide backstop (Client.Timeout) - not a deadline the caller
// set. The distinction is the difference between "the model was still working
// when our own transport gave up on it" and "the budget this call was given ran
// out", and every layer that confuses the two tells the operator to tune a
// timeout that was never the cause.
//
// The two are hard to tell apart because net/http's timeout errors define
//
//	func (e *timeoutError) Is(err error) bool { return err == context.DeadlineExceeded }
//
// so errors.Is(err, context.DeadlineExceeded) reports true for both. What
// separates them is IDENTITY: a real context deadline carries the
// context.DeadlineExceeded VALUE somewhere in its chain, while a transport
// stage timeout only claims equality with it and carries nothing. TestStdlib
// TimerErrorIdentities pins that property against the live standard library,
// so a future Go release cannot re-arm the confusion silently.
//
// Three callers depend on this separation and each got it wrong before:
// IsTransient (a stage timeout never delivered an answer, so it IS transient),
// retryRoundTripper.isRetryable (a stage timeout earns a retry; a spent budget
// does not), and the subagent layer's terminal-status vocabulary ("timed_out"
// must mean a configured deadline fired).
func IsTransportStageTimeout(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) && !carriesDeadlineIdentity(err)
}

// carriesDeadlineIdentity walks the error chain looking for the
// context.DeadlineExceeded value itself. It deliberately does NOT use
// errors.Is: the whole point is to ignore an Is method that merely claims
// equality. Both unwrap shapes are followed so a joined or multi-wrapped
// deadline is still recognized as the real thing.
func carriesDeadlineIdentity(err error) bool {
	for err != nil {
		if err == context.DeadlineExceeded {
			return true
		}
		switch unwrapper := err.(type) {
		case interface{ Unwrap() error }:
			err = unwrapper.Unwrap()
		case interface{ Unwrap() []error }:
			for _, branch := range unwrapper.Unwrap() {
				if carriesDeadlineIdentity(branch) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}
