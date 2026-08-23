package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestBuildAgentLoopOptions_ProjectsModelAndSteps locks the
// one-to-one mapping the SDK adapter exposes: Model -> Model and
// MaxSteps -> MaxIterations. Every other Options field stays at its
// SDK zero value because the SDK Options struct has no carrier for
// them.
func TestBuildAgentLoopOptions_ProjectsModelAndSteps(t *testing.T) {
	opts := Options{Model: "test-model", MaxSteps: 5}
	got := buildAgentLoopOptions(opts)
	if got.Model != "test-model" {
		t.Fatalf("buildAgentLoopOptions(%+v).Model = %q, want %q", opts, got.Model, "test-model")
	}
	if got.MaxIterations != 5 {
		t.Fatalf("buildAgentLoopOptions(%+v).MaxIterations = %d, want 5", opts, got.MaxIterations)
	}
}

// TestBuildAgentLoopOptions_EmptyRequest locks the zero-input
// behavior: a zero-value Options must produce a zero-value
// agentloop.Options, so a future caller can rely on the bridge as a
// pure projection.
func TestBuildAgentLoopOptions_EmptyRequest(t *testing.T) {
	opts := Options{}
	got := buildAgentLoopOptions(opts)
	if got.Model != "" {
		t.Fatalf("buildAgentLoopOptions(zero).Model = %q, want \"\"", got.Model)
	}
	if got.MaxIterations != 0 {
		t.Fatalf("buildAgentLoopOptions(zero).MaxIterations = %d, want 0", got.MaxIterations)
	}
}

// TestRunAgentLoop_NewFailsOnInvalidOptions locks the SDK's
// construction-time validation path: agentloop.New refuses Options
// that fail Validate, and RunAgentLoop must surface that error
// rather than silently proceeding. MaxSteps:0 maps to
// MaxIterations:0, which the SDK rejects. The exact sentinel is
// not asserted because Validate checks Completer before
// MaxIterations and the adapter does not bind a Completer today;
// the test only asserts that the construction gate fails closed.
func TestRunAgentLoop_NewFailsOnInvalidOptions(t *testing.T) {
	_, err := RunAgentLoop(context.Background(), Options{MaxSteps: 0})
	if err == nil {
		t.Fatal("RunAgentLoop(MaxSteps:0) returned nil error; want construction-time error")
	}
	if !errors.Is(err, sdkagentloop.ErrMaxIterations) && !errors.Is(err, sdkagentloop.ErrNoCompleter) {
		t.Fatalf("RunAgentLoop(MaxSteps:0) err = %v, want ErrMaxIterations or ErrNoCompleter", err)
	}
}

// TestWiringSetsSDKReasoningEffortOnRequest locks the B.2 #8
// bridge: when internal/provider/reasoning.go's encoder runs on a
// Request carrying ReasoningLevel=High, the request's
// SDKReasoningEffort must equal the SDK's ReasoningEffortHigh
// constant. The wiring path is exactly one place - the encoder - so
// the test exercises the projection end-to-end.
func TestWiringSetsSDKReasoningEffortOnRequest(t *testing.T) {
	c := provider.NewOpenAICompat("openrouter", "https://example.test", "test-key", "", "")
	req := provider.Request{Model: "test-model", ReasoningLevel: reasoning.High}
	_ = c.ReasoningFields(&req)
	if req.SDKReasoningEffort != sdkshape.ReasoningEffortHigh {
		t.Fatalf("after encoder, req.SDKReasoningEffort = %q, want %q",
			req.SDKReasoningEffort, sdkshape.ReasoningEffortHigh)
	}
}
