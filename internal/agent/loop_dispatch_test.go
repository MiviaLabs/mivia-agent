package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRunOnceDispatchesToLegacyOnEmptyBackend asserts that a default
// Options{} (Backend empty) reaches the legacy branch through runOnce.
// The scripted completer returns "legacy-output" so we can distinguish
// the dispatcher outcome from the SDK stub.
func TestRunOnceDispatchesToLegacyOnEmptyBackend(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{
		steps: []provider.Response{{Content: "legacy-output", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: reg}

	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1})
	if err != nil {
		t.Fatalf("legacy path failed: %v", err)
	}
	if got != "legacy-output" {
		t.Fatalf("got %q, want %q", got, "legacy-output")
	}
}

// TestRunOnceDispatchesToLegacyOnExplicitLegacyBackend asserts that
// Options{Backend: "legacy"} reaches the legacy branch through runOnce,
// proving the dispatcher's explicit-value path is wired identically to
// the default-zero path.
func TestRunOnceDispatchesToLegacyOnExplicitLegacyBackend(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{
		steps: []provider.Response{{Content: "legacy-output", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: reg}

	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1, Backend: "legacy"})
	if err != nil {
		t.Fatalf("legacy path failed: %v", err)
	}
	if got != "legacy-output" {
		t.Fatalf("got %q, want %q", got, "legacy-output")
	}
}

// TestRunOnceReturnsErrSDKBackendUnwirenedForSDKBackend asserts that
// Options{Backend: "sdk"} returns errSDKBackendUnwirened from the
// dispatcher, so an opt-in fails closed until the SDK completer wrapper,
// options adapter, and steer bridge land. The assertion uses errors.Is so
// the sentinel identity is the contract, not its message.
func TestRunOnceReturnsErrSDKBackendUnwirenedForSDKBackend(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}

	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1, Backend: "sdk"})
	if err == nil {
		t.Fatalf("expected error from sdk branch, got nil (text=%q)", got)
	}
	if !errors.Is(err, errSDKBackendUnwirened) {
		t.Fatalf("err=%v, want errors.Is(err, errSDKBackendUnwirened)", err)
	}
	if got != "" {
		t.Fatalf("sdk branch returned text %q, want empty", got)
	}
}

// TestRunOnceRejectsUnknownBackend asserts that any Backend value other
// than "", "legacy", or "sdk" surfaces an error whose message names the
// bad value AND both accepted values, so the operator can see what they
// typed and what the dispatcher would have accepted.
func TestRunOnceRejectsUnknownBackend(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}

	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1, Backend: "bogus"})
	if err == nil {
		t.Fatalf("expected error from unknown-backend branch, got nil (text=%q)", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Fatalf("err message %q does not name the bad value %q", msg, "bogus")
	}
	if !strings.Contains(msg, "legacy") {
		t.Fatalf("err message %q does not name the accepted value %q", msg, "legacy")
	}
	if !strings.Contains(msg, "sdk") {
		t.Fatalf("err message %q does not name the accepted value %q", msg, "sdk")
	}
	if got != "" {
		t.Fatalf("unknown-backend branch returned text %q, want empty", got)
	}
}
