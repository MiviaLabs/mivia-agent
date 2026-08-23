package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestBuildAgentLoopOptions_ProjectsModelAndSteps locks the mapping
// the SDK adapter exposes: Model -> Model, MaxSteps -> MaxIterations,
// and the Loop's Completer and Tools land on the SDK Options wrapped
// and converted.
func TestBuildAgentLoopOptions_ProjectsModelAndSteps(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, err := buildAgentLoopOptions(l, Options{Model: "test-model", MaxSteps: 5})
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Model != "test-model" {
		t.Fatalf("Model = %q, want %q", got.Model, "test-model")
	}
	if got.MaxIterations != 5 {
		t.Fatalf("MaxIterations = %d, want 5", got.MaxIterations)
	}
	if got.Completer == nil {
		t.Fatal("Completer = nil, want the wrapped CLI completer")
	}
	if got.Tools == nil {
		t.Fatal("Tools = nil, want the converted registry")
	}
}

// TestBuildAgentLoopOptions_EmptyRequest locks the zero-input
// behavior: a zero-value Options maps MaxSteps 0 to the documented
// default of 25, because the SDK's Validate requires a positive
// MaxIterations and 0 is the most common unset configuration.
func TestBuildAgentLoopOptions_EmptyRequest(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, err := buildAgentLoopOptions(l, Options{})
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("Model = %q, want empty", got.Model)
	}
	if got.MaxIterations != defaultSDKMaxIterations {
		t.Fatalf("MaxIterations = %d, want default %d", got.MaxIterations, defaultSDKMaxIterations)
	}
}

// TestRunAgentLoop_FailsOnNilCompleter locks the fail-closed path:
// RunAgentLoop delegates to RunAgentLoopOnce with a zero Loop, whose
// nil Completer is rejected by the wrapper constructor before the
// SDK's own Validate runs.
func TestRunAgentLoop_FailsOnNilCompleter(t *testing.T) {
	_, err := RunAgentLoop(context.Background(), Options{})
	if err == nil {
		t.Fatal("RunAgentLoop(zero Loop) returned nil error; want nil-completer error")
	}
	if !strings.Contains(err.Error(), "nil CLI completer") {
		t.Fatalf("err = %v, want it to name the nil CLI completer", err)
	}
	if !errors.Is(err, sdkagentloop.ErrNoCompleter) {
		// The wrapper error is its own error; the SDK sentinel is not
		// expected here - the assertion documents that the failure
		// happens at the wrapper, before Validate.
		t.Logf("note: err does not wrap ErrNoCompleter (fails at wrapper): %v", err)
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
