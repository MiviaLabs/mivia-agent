package agent

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRunOnceRunsSDKBackend asserts that runOnce always drives the
// SDK-backed loop through runOnceSDK: the fake completer's ChatTurn
// content comes back as the turn result. The legacy pre-SDK engine
// and its Backend flag are gone (loop_dispatch.go); this is the
// dispatcher's only path now.
func TestRunOnceRunsSDKBackend(t *testing.T) {
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

// TestRunOnceSDKCarriesMaxToolCallsThroughRun asserts that
// WorkLimits.MaxToolCalls - once the one CLI Options field the SDK
// path could not carry - now runs to completion through the public
// Run, via the ToolBudget bridge (agentloop_toolbudget.go). See
// TestSDKDefaultBackendEnforcesCumulativeMaxToolCalls
// (agentloop_toolbudget_test.go) for the enforcement path.
func TestRunOnceSDKCarriesMaxToolCallsThroughRun(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "sdk-output", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{Model: "m", MaxSteps: 1}
	opts.WorkLimits.MaxToolCalls = 1
	got, err := loop.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run with WorkLimits.MaxToolCalls: %v, want nil (carried)", err)
	}
	if got != "sdk-output" {
		t.Fatalf("got %q, want %q", got, "sdk-output")
	}
}
