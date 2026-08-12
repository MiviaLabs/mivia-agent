package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

// TestSafeInvokeRecoversPanicAndContinuesRetryLoop is a bug-audit coverage
// gap (test-coverage lens): existing panic tests (internal/subagents) prove a
// panic surfaces as a failed Result, but never exercise Dispatcher.execute's
// OWN attempt loop (Request.Retry-driven) - whether a recovered panic on one
// attempt lets the loop continue to a later attempt, or gets mistaken for a
// context cancellation and stops early. This panics on the first invocation
// and succeeds on the second, with Retry: 1, and requires the second attempt
// to actually run.
func TestSafeInvokeRecoversPanicAndContinuesRetryLoop(t *testing.T) {
	d := New(Policy{MaxRetries: 1})
	attempt := 0
	_ = d.Register(Subagent, "panics-then-succeeds", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		attempt++
		if attempt == 1 {
			panic("simulated panic on first attempt")
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))

	r := d.Invoke(context.Background(), Request{Kind: Subagent, Name: "panics-then-succeeds", Retry: 1})
	if r.Err != nil {
		t.Fatalf("Invoke Err = %v, want nil (the retry loop must continue past a recovered panic to a succeeding attempt)", r.Err)
	}
	if attempt != 2 {
		t.Fatalf("attempt count = %d, want 2 (handler must have been called again after the panic was recovered)", attempt)
	}
	if r.Attempts != 2 {
		t.Fatalf("r.Attempts = %d, want 2", r.Attempts)
	}
}
