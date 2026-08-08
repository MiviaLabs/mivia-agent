package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// HTTP 408 is retried by the transport (retry.go: case StatusRequestTimeout),
// so when the transport budget is spent the error must still read as transient
// to the step layer. The "http 408" phrase was missing from transientMessages,
// which made a 408 end a step the transport itself would have retried.
func TestHTTP408MessageIsTransient(t *testing.T) {
	if !IsTransient(errors.New("zai: provider error (HTTP 408)")) {
		t.Fatal("an HTTP 408 message must be transient: the transport retries 408s")
	}
}

// markPermanent is the typed counterpart of TransientError: it pins a failure
// as permanent so no text phrase can flip it back to transient. The marker must
// win over BOTH the message table and the TransientError type test, because a
// permanent provider block must never re-run a whole step.
func TestPermanentMarkerIsNotTransient(t *testing.T) {
	marked := markPermanent(fmt.Errorf("zai: provider error (HTTP 429, code 1113: insufficient balance or no resource package)"))
	if marked == nil {
		t.Fatal("markPermanent must wrap a non-nil error")
	}
	if IsTransient(marked) {
		t.Fatalf("a permanent-marked error must not be transient, even with 429 text: %v", marked)
	}
	// An error cannot be both marked: the permanent marker must also beat the
	// TransientError type test, which otherwise wins on the type alone.
	if IsTransient(&TransientError{Err: marked}) {
		t.Fatal("a permanent marker inside a TransientError must still not be transient")
	}
	if markPermanent(nil) != nil {
		t.Fatal("markPermanent(nil) must return nil")
	}
}

// A deadline the provider marked transient at the read site must win over the
// blanket rule that a bare context deadline is not transient. Without this,
// the step-level retry (runStepWithTransientRetry) never engages for a
// transport-backstop read timeout, and the run dies terminal timed_out
// instead of reacting to a cut call.
func TestProviderMarkedDeadlineIsTransient(t *testing.T) {
	marked := &TransientError{Err: context.DeadlineExceeded}
	if !IsTransient(marked) {
		t.Fatal("a provider-marked read deadline must be transient: the call never delivered an answer")
	}
	if !IsTransient(fmt.Errorf("openrouter: read response: %w (request deadline 12h0m0s)", marked)) {
		t.Fatal("a wrapped provider-marked read deadline must stay transient")
	}
	// A bare deadline stays permanent: step and run deadlines surface this way.
	if IsTransient(context.DeadlineExceeded) {
		t.Fatal("a bare context deadline must not be transient")
	}
}

// markTransientReadDeadline must mark a deadline transient only when the fired
// bound was tighter than the provider's own request timeout (transport
// backstop or parent deadline). The provider's own request timeout expiring is
// a permanent statement and must pass through unchanged.
func TestMarkTransientReadDeadline(t *testing.T) {
	// Armed request deadline still in the future: a tighter bound fired.
	armed, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	got := markTransientReadDeadline(armed, 12*time.Hour, context.DeadlineExceeded)
	var transient *TransientError
	if !errors.As(got, &transient) {
		t.Fatalf("markTransientReadDeadline = %v, want TransientError", got)
	}

	// The provider's own request deadline fired: armed deadline has passed.
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	if got := markTransientReadDeadline(expired, 12*time.Hour, context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("expired armed deadline = %v, want the bare deadline preserved", got)
	}

	// No armed timeout: a deadline is the parent's; never mark it.
	if got := markTransientReadDeadline(context.Background(), 0, context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("no armed timeout = %v, want unchanged", got)
	}

	// A non-deadline error passes through unchanged even under a live deadline.
	plain := errors.New("connection reset")
	if got := markTransientReadDeadline(armed, 12*time.Hour, plain); !errors.Is(got, plain) {
		t.Fatalf("non-deadline error = %v, want unchanged", got)
	}
}
