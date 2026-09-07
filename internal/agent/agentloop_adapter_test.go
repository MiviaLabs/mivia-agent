package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
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

// TestInstallSDKEventBridge_HeartbeatRidesBus pins the heartbeat
// adoption row's coupling: the 15s interval installs only next to the
// bridged Bus (a positive HeartbeatInterval without a Bus fails the
// SDK's Validate), and a run with no observer gets neither.
func TestInstallSDKEventBridge_HeartbeatRidesBus(t *testing.T) {
	turn := newSDKTurnState()
	wired := sdkagentloop.Options{}
	installSDKEventBridge(&wired, Options{OnEvent: func(Event) {}}, turn)
	if wired.Bus == nil {
		t.Fatal("Bus = nil with OnEvent wired; the event bridge must install")
	}
	if wired.HeartbeatInterval != sdkHeartbeatInterval {
		t.Fatalf("HeartbeatInterval = %s, want %s", wired.HeartbeatInterval, sdkHeartbeatInterval)
	}
	headless := sdkagentloop.Options{}
	installSDKEventBridge(&headless, Options{}, turn)
	if headless.Bus != nil || headless.HeartbeatInterval != 0 {
		t.Fatalf("headless run got Bus=%v HeartbeatInterval=%s; want neither",
			headless.Bus, headless.HeartbeatInterval)
	}
}

// TestApplySDKTrimStandsDownOnAdoptedCompaction pins the Trim row:
// when the SDK compaction triple owns the window (ceiling +
// summarizer, no PreparationManager) the host Trim stands down - the
// SDK's Window and Trim are mutually exclusive - and installs in
// every other case.
func TestApplySDKTrimStandsDownOnAdoptedCompaction(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	turn := newSDKTurnState()
	adopted := Options{
		Model:            "m",
		MaxContextTokens: 1000,
		SummaryConfig:    SummaryConfig{Summarizer: &contextmgr.Summarizer{}},
	}
	var out sdkagentloop.Options
	applySDKTrim(l, adopted, turn, &out)
	if out.Trim != nil {
		t.Fatal("Trim installed although SDK compaction is adopted; Window and Trim are mutually exclusive")
	}
	unadopted := Options{
		Model:              "m",
		MaxContextTokens:   1000,
		PreparationManager: &stubPreparationManager{keep: 3},
	}
	applySDKTrim(l, unadopted, turn, &out)
	if out.Trim == nil {
		t.Fatal("Trim not installed although compaction is not adopted (no summarizer)")
	}
}

// TestBuildAgentLoopOptions_NoWindowWithPreparationManager pins the
// compaction row's negative case: a wired PreparationManager disables
// the SDK compaction triple even with a ceiling and a summarizer, so
// the SDK's Window stays nil.
func TestBuildAgentLoopOptions_NoWindowWithPreparationManager(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{
		Model:              "m",
		MaxContextTokens:   1000,
		SummaryConfig:      SummaryConfig{Summarizer: &contextmgr.Summarizer{}},
		PreparationManager: &stubPreparationManager{keep: 3},
	}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Window != nil {
		t.Fatal("Window set although a PreparationManager is wired; the SDK triple must stay off")
	}
}

// TestAgentLoopCompleterEstimateTokens pins the TokenEstimator
// adapter the SDK compaction trigger depends on (EnableCompaction
// fails closed without one): EstimateTokens converts the SDK request
// back to the CLI shape and returns exactly the host estimator's
// number, and a nil completer fails closed instead of panicking.
func TestAgentLoopCompleterEstimateTokens(t *testing.T) {
	c, err := newAgentLoopCompleterWithDefaults(&fakeCompleter{name: "test"}, turnRequestDefaults{}, nil, nil, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("newAgentLoopCompleterWithDefaults: %v", err)
	}
	req := sdkshape.Request{Messages: []sdkshape.Message{{Role: sdkshape.RoleUser, Content: "estimate me"}}}
	got, err := c.EstimateTokens(req)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	want, err := provider.EstimatePromptCost(sdkMessagesToCLI(req.Messages), sdkToolDefsToCLI(req.Tools), c.ctxProfile)
	if err != nil {
		t.Fatalf("EstimatePromptCost: %v", err)
	}
	if got != want {
		t.Fatalf("EstimateTokens = %d, want the host estimator's %d", got, want)
	}
	var nilC *agentLoopCompleter
	if _, err := nilC.EstimateTokens(req); err == nil {
		t.Fatal("nil completer EstimateTokens returned nil error; want fail-closed")
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
	if got.Bounds.MaxTotalTokens != 0 {
		t.Fatalf("MaxTotalTokens = %d, want 0 (uncapped): the SDK bound counts cumulative billed tokens across the whole run, so deriving it from the per-prompt context ceiling hard-fails healthy long turns that re-bill history every iteration", got.Bounds.MaxTotalTokens)
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

// TestBuildAgentLoopOptions_SDKCompactionAdopted locks the compaction
// triple row: with a context ceiling and a wired summarizer, the SDK
// Options carry a Window sized from the ceiling, a Summarizer over
// the wrapped completer, and a Calibrated estimator - and the host
// Trim pass stands down, because the SDK's Window and Trim are
// mutually exclusive.
func TestBuildAgentLoopOptions_SDKCompactionAdopted(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	opts := Options{
		SessionID:        "sess-compact",
		MaxContextTokens: 10000,
	}
	opts.SummaryConfig.Summarizer = &contextmgr.Summarizer{}
	got, _, err := buildAgentLoopOptions(l, opts, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Window == nil {
		t.Fatal("Window = nil, want the SDK compaction window")
	}
	if got.Window.MaxTokens != 10000 || got.Window.Reserve != 2000 {
		t.Fatalf("Window = %+v, want MaxTokens 10000 and Reserve 2000", got.Window)
	}
	if got.Summarizer == nil {
		t.Fatal("Summarizer = nil, want the SDK summarizer over the wrapped completer")
	}
	if got.Calibrated == nil {
		t.Fatal("Calibrated = nil, want the calibrated estimator")
	}
	if got.Trim != nil {
		t.Fatal("Trim set while the SDK compaction triple owns the window")
	}
}

// TestBuildAgentLoopOptions_SDKCompactionNeedsSummarizer pins the
// adoption precondition: without a wired summarizer the SDK cannot
// own summarization, so no Window lands even with a ceiling.
func TestBuildAgentLoopOptions_SDKCompactionNeedsSummarizer(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	opts := Options{SessionID: "sess-plain", MaxContextTokens: 10000}
	got, _, err := buildAgentLoopOptions(l, opts, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.Window != nil {
		t.Fatal("Window set without a wired summarizer; SDK compaction cannot adopt")
	}
	if got.Summarizer != nil || got.Calibrated != nil {
		t.Fatal("triple partially wired without adoption")
	}
}
