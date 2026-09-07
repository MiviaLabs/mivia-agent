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
	got, _, err := buildAgentLoopOptions(l, Options{Model: "test-model", MaxSteps: 5}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Model != "test-model" {
		t.Fatalf("Model = %q, want %q", got.Model, "test-model")
	}
	if got.Bounds.MaxIterations != 5 {
		t.Fatalf("MaxIterations = %d, want 5", got.Bounds.MaxIterations)
	}
	if got.Completer == nil {
		t.Fatal("Completer = nil, want the wrapped CLI completer")
	}
	if got.Tools == nil {
		t.Fatal("Tools = nil, want the converted registry")
	}
}

// TestBuildAgentLoopOptions_EmptyRequest locks the zero-input
// behavior: a zero-value Options passes MaxSteps 0 through to the SDK,
// which the SDK's Validate accepts and treats as uncapped (matches the
// legacy loop's MaxSteps <= 0 == unbounded contract; see
// mivia-ai-sdk/agentloop.New's defaulting via unboundedOrSet). The
// adapter no longer substitutes a finite default.
func TestBuildAgentLoopOptions_EmptyRequest(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("Model = %q, want empty", got.Model)
	}
	if got.Bounds.MaxIterations != 0 {
		t.Fatalf("MaxIterations = %d, want 0 (unbounded passes through to SDK)", got.Bounds.MaxIterations)
	}
}

// TestRunAgentLoop_FailsOnNilCompleter locks the fail-closed path:
// RunAgentLoop delegates to RunAgentLoopOnce with a zero Loop, whose
// nil Completer is rejected by the wrapper constructor before the
// SDK's own Validate runs.
func TestRunAgentLoop_FailsOnNilCompleter(t *testing.T) {
	_, err := RunAgentLoop(context.Background(), &Loop{}, Options{})
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

// TestBuildAgentLoopOptions_AdoptionRows pins the adoption-table
// projection: Usage (+ SessionID), Budget, Bounds.MaxTotalTokens,
// Bounds.MaxConsecutiveToolFailures, DedupWithinTurn, and Tracer all
// land on the built SDK Options. See agentloop_adoption.go.
func TestBuildAgentLoopOptions_AdoptionRows(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, turn, err := buildAgentLoopOptions(l, Options{
		SessionID:        "sess-1",
		MaxContextTokens: 500000,
	}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Usage == nil {
		t.Fatal("Usage = nil, want the SDK session accumulator")
	}
	if got.Budget == nil || got.Budget.MaxBytes != sdkSessionBudgetMaxBytes {
		t.Fatalf("Budget = %+v, want the runaway bound", got.Budget)
	}
	if got.Budget.MaxEvents != sdkSessionBudgetMaxEvents {
		t.Fatalf("Budget.MaxEvents = %d, want %d", got.Budget.MaxEvents, sdkSessionBudgetMaxEvents)
	}
	if got.Bounds.MaxTotalTokens != 500000 {
		t.Fatalf("MaxTotalTokens = %d, want the context ceiling", got.Bounds.MaxTotalTokens)
	}
	if got.Bounds.MaxConsecutiveToolFailures != sdkFailureSpiralBound {
		t.Fatalf("MaxConsecutiveToolFailures = %d, want %d", got.Bounds.MaxConsecutiveToolFailures, sdkFailureSpiralBound)
	}
	if got.Tracer == nil {
		t.Fatal("Tracer = nil, want the run span tracer")
	}
	if turn.tracer != got.Tracer {
		t.Fatal("turn state did not park the run tracer")
	}
	if got.Audit != nil {
		t.Fatal("Audit set without the operator audit directory; it must stay inert")
	}
}

// TestBuildAgentLoopOptions_AdoptionRowsAuditSink locks the audit
// row: with the operator audit directory named, the SDK loop's Audit
// hook feeds the sdkloop JSONL sink.
func TestBuildAgentLoopOptions_AdoptionRowsAuditSink(t *testing.T) {
	t.Setenv(EnvProviderAuditDir, t.TempDir())
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{SessionID: "sess-audit"}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Audit == nil {
		t.Fatal("Audit = nil with an audit directory named; the SDK loop audit row did not adopt")
	}
}

// TestBuildAgentLoopOptions_AdoptionRowsBlankSession locks the guard:
// a blank SessionID leaves Usage unset, because the SDK's Validate
// rejects Usage without a SessionID and the turn must still build.
func TestBuildAgentLoopOptions_AdoptionRowsBlankSession(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Usage != nil {
		t.Fatal("Usage set without a SessionID; SDK Validate would reject the turn")
	}
	if got.Budget == nil || got.Budget.MaxBytes != sdkSessionBudgetMaxBytes {
		t.Fatal("Budget must be set even without a ceiling; it bounds a runaway loop")
	}
}
