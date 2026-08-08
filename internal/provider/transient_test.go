package provider

import (
	"errors"
	"fmt"
	"testing"
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
