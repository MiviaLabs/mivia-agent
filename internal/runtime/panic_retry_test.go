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

// panicEphemeralHandler succeeds on Invoke but panics from
// EphemeralResultMarker, the side interface Dispatcher.execute calls AFTER
// the handler already succeeded, to build the event-log output preview.
type panicEphemeralHandler struct{}

func (panicEphemeralHandler) Invoke(context.Context, Request) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func (panicEphemeralHandler) EphemeralResultMarker(Request) string {
	panic("simulated EphemeralResultMarker panic")
}

// TestInvokeRecoversPanicFromEphemeralResultMarker is a bug-audit finding
// (hostile panic-recovery lens): safeInvoke only wraps h.Invoke, but
// Dispatcher.execute calls ephemeralMarker(h, req) - which invokes the
// handler-supplied EphemeralResultMarker method - on the SUCCESS path,
// outside safeInvoke's recovery. A handler implementing that side interface
// and panicking from it defeated the whole point of the panic-recovery fix:
// the task actually succeeded (Invoke returned a real answer), but building
// its output preview crashed the process anyway.
func TestInvokeRecoversPanicFromEphemeralResultMarker(t *testing.T) {
	d := New(Policy{})
	_ = d.Register(Tool, "ephemeral-panics", panicEphemeralHandler{})

	r := d.Invoke(context.Background(), Request{Kind: Tool, Name: "ephemeral-panics"})
	if r.Err != nil {
		t.Fatalf("Invoke Err = %v, want nil - the handler's actual answer must survive a panic in EphemeralResultMarker", r.Err)
	}
	if string(r.Output) != `{"ok":true}` {
		t.Fatalf("Invoke Output = %s, want the handler's real answer preserved", r.Output)
	}
}
