package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRunOnceDispatchesToSDKOnEmptyBackend asserts that a default
// Options{} (Backend empty) reaches the SDK branch through runOnce.
// The fake completer's ChatTurn returns "sdk-output" so the test can
// distinguish the dispatcher outcome from a legacy completer. The
// empty-Value-is-SDK mapping is the SDK convergence's default
// flip: production callers that do not set Backend now run the
// SDK-backed loop.
func TestRunOnceDispatchesToSDKOnEmptyBackend(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "sdk-output", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1})
	if err != nil {
		t.Fatalf("sdk path failed: %v", err)
	}
	if got != "sdk-output" {
		t.Fatalf("got %q, want %q", got, "sdk-output")
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

// TestRunOnceDispatchesToSDKBackend asserts that Options{Backend:
// "sdk"} drives the SDK-backed loop end to end through the public
// (*Loop).Run: the fake completer's ChatTurn content comes back as
// the turn result. This is the commit-4 wiring test - the flag
// reaches runOnceSDK, which reaches RunAgentLoopOnce, which drives
// the SDK loop over the wrapped completer and converted registry.
func TestRunOnceDispatchesToSDKBackend(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "sdk-output", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	got, err := loop.Run(context.Background(), "hi", Options{Model: "m", MaxSteps: 1, Backend: "sdk"})
	if err != nil {
		t.Fatalf("sdk path failed: %v", err)
	}
	if got != "sdk-output" {
		t.Fatalf("got %q, want %q", got, "sdk-output")
	}
}

// TestRunOnceSDKFailClosedSurfacesThroughRun asserts that a CLI
// Options field the SDK path cannot carry (MaxContextTokens)
// surfaces the fail-closed error through the public Run, so an
// opt-in caller learns the boundary at the call.
func TestRunOnceSDKFailClosedSurfacesThroughRun(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake"},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{Model: "m", MaxSteps: 1, Backend: "sdk"}
	opts.PreserveWorkLimits = true
	_, err := loop.Run(context.Background(), "hi", opts)
	if err == nil {
		t.Fatal("Run with PreserveWorkLimits under the sdk backend returned nil error; want fail-closed error")
	}
	if !strings.Contains(err.Error(), "PreserveWorkLimits") {
		t.Fatalf("err = %v, want it to name PreserveWorkLimits", err)
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
