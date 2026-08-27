package sdkadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// TestNewHandlerAllowedPreToolUse confirms the bridge hands the merged
// Outcome to the SDK handler with the verdict mapping rules from
// hooks.go: PreToolUse without Denied returns allow, no error.
func TestNewHandlerAllowedPreToolUse(t *testing.T) {
	runner := &fakeRunner{outcome: hooks.Outcome{Denied: false}}
	groups := []hooks.Group{{Event: hooks.EventPreToolUse, Handlers: []hooks.Handler{{Type: "command"}}}}
	wrapped := NewHandler(runner, groups)
	allow, err := wrapped.Handle(context.Background(), map[string]any{"tool": "x"})
	if err != nil {
		t.Fatalf("Handle allow must not error, got %v", err)
	}
	if !allow {
		t.Fatalf("Handle allow must return true, got false")
	}
}

// TestNewHandlerDeniedPreToolUseVeto pins the verdict mapping: a CLI
// Outcome with Denied=true and a Reason is a veto; the SDK handler
// returns (false, nil) so the wrapping Registry.Fire raises ErrVetoed.
func TestNewHandlerDeniedPreToolUseVeto(t *testing.T) {
	runner := &fakeRunner{outcome: hooks.Outcome{Denied: true, Reason: "blocked by policy"}}
	wrapped := NewHandler(runner, []hooks.Group{{Event: hooks.EventPreToolUse, Handlers: []hooks.Handler{{Type: "command"}}}})
	allow, err := wrapped.Handle(context.Background(), map[string]any{"tool": "x"})
	if err != nil {
		t.Fatalf("Handle veto must not return error, got %v", err)
	}
	if allow {
		t.Fatalf("Handle veto must return allow=false, got %v", allow)
	}
}

// TestNewHandlerDeniedWithoutReasonReturnsError confirms the edge case
// where the CLI Outcome is denied but has no Reason: this is a defect
// in the hook runner and the bridge surfaces it as a non-nil error so
// the SDK's Registry.Fire log records a real failure reason rather
// than an unsourced veto.
func TestNewHandlerDeniedWithoutReasonReturnsError(t *testing.T) {
	runner := &fakeRunner{outcome: hooks.Outcome{Denied: true}}
	wrapped := NewHandler(runner, []hooks.Group{{Event: hooks.EventPreToolUse, Handlers: []hooks.Handler{{Type: "command"}}}})
	allow, err := wrapped.Handle(context.Background(), map[string]any{"tool": "x"})
	if err == nil {
		t.Fatalf("Handle veto without reason must return error")
	}
	if allow {
		t.Fatalf("Handle veto without reason must return allow=false")
	}
}

// TestNewHandlerReactiveAlwaysAllows pins the reactive-event rule:
// PostToolUse and Stop cannot veto a tool call that already ran. Even
// if the CLI Outcome has Denied=true (a legacy hook script asserting
// block semantics that no longer exist), the bridge returns allow.
func TestNewHandlerReactiveAlwaysAllows(t *testing.T) {
	for _, event := range []hooks.Event{hooks.EventPostToolUse, hooks.EventStop} {
		t.Run(string(event), func(t *testing.T) {
			runner := &fakeRunner{outcome: hooks.Outcome{Denied: true, Reason: "ignored"}}
			wrapped := NewHandler(runner, []hooks.Group{{Event: event, Handlers: []hooks.Handler{{Type: "command"}}}})
			payload := map[string]any{"event": string(event), "tool": "x"}
			allow, err := wrapped.Handle(context.Background(), payload)
			if err != nil {
				t.Fatalf("Handle reactive event must not return error, got %v", err)
			}
			if !allow {
				t.Fatalf("Handle reactive event %q must allow, got veto", event)
			}
		})
	}
}

// TestNewHandlerReasonWithoutDeniedAllows confirms the no-Denied path
// is an allow: Outcome.Reason is documented as model-visible block
// reason, but without Denied set it is advisory context, not a veto.
func TestNewHandlerReasonWithoutDeniedAllows(t *testing.T) {
	runner := &fakeRunner{outcome: hooks.Outcome{Reason: "advisory text"}}
	wrapped := NewHandler(runner, []hooks.Group{{Event: hooks.EventPreToolUse, Handlers: []hooks.Handler{{Type: "command"}}}})
	allow, err := wrapped.Handle(context.Background(), map[string]any{"tool": "x"})
	if err != nil {
		t.Fatalf("Handle without Denied must not error, got %v", err)
	}
	if !allow {
		t.Fatalf("Handle without Denied must allow, got veto")
	}
}

// TestNewHandlerWarningsOnlyAllows confirms that warnings are operator
// diagnostics and never reach the SDK veto path: Outcome.Warnings
// without Denied or Reason is an allow.
func TestNewHandlerWarningsOnlyAllows(t *testing.T) {
	runner := &fakeRunner{outcome: hooks.Outcome{Warnings: []string{"operator warning"}}}
	wrapped := NewHandler(runner, []hooks.Group{{Event: hooks.EventPreToolUse, Handlers: []hooks.Handler{{Type: "command"}}}})
	allow, err := wrapped.Handle(context.Background(), map[string]any{"tool": "x"})
	if err != nil {
		t.Fatalf("warnings without Denied must not error, got %v", err)
	}
	if !allow {
		t.Fatalf("warnings without Denied must allow, got veto")
	}
}

// TestPayloadFromAnyAcceptsMap confirms the primary payload shape: an
// opaque map[string]any reaches the hook runner unchanged.
func TestPayloadFromAnyAcceptsMap(t *testing.T) {
	p := map[string]any{"event": "PreToolUse", "tool": "run_command", "session_id": "abc"}
	got, err := PayloadFromAny(p)
	if err != nil {
		t.Fatalf("PayloadFromAny: %v", err)
	}
	if got.Event != hooks.EventPreToolUse || got.Tool != "run_command" || got.SessionID != "abc" {
		t.Fatalf("PayloadFromAny mismatch: %+v", got)
	}
}

// TestPayloadFromAnyJSONRawMessage confirms that a JSON-encoded payload
// reaches the bridge by way of a byte slice. The bytes are the wire
// shape produced by the SDK; the bridge must read them.
func TestPayloadFromAnyJSONRawMessage(t *testing.T) {
	raw := []byte(`{"event":"PreToolUse","tool":"run_command","session_id":"abc"}`)
	got, err := PayloadFromAny(raw)
	if err != nil {
		t.Fatalf("PayloadFromAny: %v", err)
	}
	if got.Event != hooks.EventPreToolUse || got.Tool != "run_command" || got.SessionID != "abc" {
		t.Fatalf("PayloadFromAny raw mismatch: %+v", got)
	}
}

// TestPayloadFromAnyRejectsUnknown confirms the bridge rejects a
// payload type it does not know: a defensive guard, since payloads
// outside the accepted shapes are a sign of a caller bug.
func TestPayloadFromAnyRejectsUnknown(t *testing.T) {
	_, err := PayloadFromAny(struct{ X int }{X: 1})
	if err == nil {
		t.Fatalf("PayloadFromAny unknown type must error")
	}
}

// fakeRunner is the bridge's pluggable hook-runner interface stand-in.
// The CLI's production hooks.Runner.Run does the subprocess work; this
// fake returns a pre-canned outcome so the verdict mapping is the only
// thing under test.
type fakeRunner struct {
	outcome hooks.Outcome
	err     error
}

func (f *fakeRunner) Run(_ context.Context, _ []hooks.Group, _ hooks.Payload) hooks.Outcome {
	return f.outcome
}

// Compile-time check that the bridge value can be wrapped into the
// SDK's Handler shape (func(ctx, any) (bool, error)) at the call site
// without an import cycle. The wrapping is intentional: the bridge
// value carries configuration (runner + groups), so the registry
// adapter closes over those at construction time.
var _ = func(_ context.Context, _ any) (bool, error) {
	h := NewHandler(&fakeRunner{}, nil)
	allow, err := h.Handle(context.Background(), nil)
	return allow, err
}
