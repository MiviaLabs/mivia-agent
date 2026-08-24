package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// usageReportingCompleter returns a fixed response carrying the configured
// token usage on every ChatTurn, so loop-level calibration behavior can be
// pinned without an HTTP round trip.
type usageReportingCompleter struct {
	usage provider.TokenUsage
}

func (c *usageReportingCompleter) Name() string { return "usage-test" }
func (c *usageReportingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "answer", nil
}
func (c *usageReportingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "answer", nil
}
func (c *usageReportingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "answer", FinishReason: "stop", TokenUsage: c.usage}, nil
}

// A reported response with zero input tokens must not enter the calibration
// EWMA: it is a cache-only / zero-accounting observation, and feeding it in
// would drag the correction ratio toward the 0.5 floor on a single turn.
func TestLoopSkipsZeroInputCalibrationUpdate(t *testing.T) {
	loop := &Loop{
		Completer: &usageReportingCompleter{usage: provider.TokenUsage{Reported: true, InputTokens: 0, OutputTokens: 10}},
		Tools:     tools.NewRegistry(),
	}
	if _, err := loop.Run(context.Background(), "hello", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if loop.Calibration.Samples != 0 || loop.Calibration.Ratio != 0 {
		t.Fatalf("zero-input response must not update calibration, got %+v", loop.Calibration)
	}
}

// The calibration denominator must be the reserve-free prompt estimate
// (EstimatePromptCost), not RequestTokens: RequestTokens folds the MaxTokens
// output allowance into the estimate, so a 1000-token output reserve would
// collapse the ratio from ~1.0 to ~0.009 and poison every downstream planner.
func TestLoopCalibrationUsesReserveFreePromptEstimate(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}
	promptEstimate, err := provider.EstimatePromptCost(msgs, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	maxTokens := 1000
	reserveInclusive, err := provider.EstimateRequestCost(msgs, nil, maxTokens, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if promptEstimate == reserveInclusive {
		t.Fatalf("test setup: reserve-inclusive estimate %d equals prompt estimate %d", reserveInclusive, promptEstimate)
	}

	loop := &Loop{
		Completer: &usageReportingCompleter{usage: provider.TokenUsage{Reported: true, InputTokens: promptEstimate, OutputTokens: 5}},
		Tools:     tools.NewRegistry(),
	}
	if _, err := loop.Run(context.Background(), "hello", Options{Model: "m", MaxSteps: 5, MaxTokens: &maxTokens}); err != nil {
		t.Fatal(err)
	}
	if loop.Calibration.Samples != 1 {
		t.Fatalf("Samples = %d, want 1", loop.Calibration.Samples)
	}
	want := float64(promptEstimate) / float64(promptEstimate)
	if loop.Calibration.Ratio < want-0.01 || loop.Calibration.Ratio > want+0.01 {
		t.Fatalf("Ratio = %f, want ~%f (reserve-free prompt denominator)", loop.Calibration.Ratio, want)
	}
}

// calibrationProbe records the PrepareInput it was handed so tests can assert
// the loop wired its rolling calibration into context planning.
type calibrationProbe struct {
	lastInput contextmgr.PrepareInput
}

func (p *calibrationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.lastInput = input
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "calibration-probe")
}

func (p *calibrationProbe) Discard(contextmgr.Preparation) {}

// A loop that has accumulated calibration samples must pass its correction
// ratio into context planning; a zero-sample loop must leave the input's
// CalibrationRatio untouched (the caller's own value wins).
func TestPrepareStepWiresCalibrationRatio(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("context-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &calibrationProbe{}
	loop := &Loop{
		Completer:   preparationSuccessCompleter{},
		Tools:       tools.NewRegistry(),
		Calibration: contextmgr.Calibration{Ratio: 1.7, Samples: 3},
	}
	if _, err := loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{Budget: 100, Principal: principal, Binding: binding},
	}); err != nil {
		t.Fatal(err)
	}
	if probe.lastInput.CalibrationRatio != 1.7 {
		t.Fatalf("CalibrationRatio = %f, want 1.7", probe.lastInput.CalibrationRatio)
	}

	// Zero-sample calibration must not override a caller-supplied ratio.
	probe = &calibrationProbe{}
	loop = &Loop{Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100, Principal: principal, Binding: binding, CalibrationRatio: 1.3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if probe.lastInput.CalibrationRatio != 1.3 {
		t.Fatalf("zero-sample loop changed CalibrationRatio to %f, want caller value 1.3", probe.lastInput.CalibrationRatio)
	}
}

// The token-usage drift event must carry the calibration ratio that was in
// effect for the turn (pre-update), not the post-update EWMA: the line
// "estimate X vs actual Y (ratio R)" must read as "we planned with R; this
// turn's raw ratio is Y/X". Emitting the post-update value makes R disagree
// with the estimate/actual pair shown on the same line. On the first turn the
// zero-value ratio (0.0, meaning unity) must be emitted as 1.00, not 0.00.
func TestLoopEmitsPreUpdateCalibrationRatio(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}
	est, err := provider.EstimatePromptCost(msgs, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	var ratios []float64
	var details []string
	loop := &Loop{
		Completer: &usageReportingCompleter{usage: provider.TokenUsage{Reported: true, InputTokens: 2 * est, OutputTokens: 5}},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{Model: "m", MaxSteps: 5, OnEvent: func(e Event) {
		if e.Kind == EventTokenUsage && e.TokenUsage != nil {
			ratios = append(ratios, e.TokenUsage.CalibrationRatio)
			details = append(details, e.Detail)
		}
	}}
	req := provider.Request{Model: "m", Messages: msgs}

	// emitTurnUsage is what requestStep (deleted with the legacy engine)
	// used to call after every completed ChatTurn; calling it directly
	// pins the same pre-update-ratio contract the SDK completer wrapper
	// now drives it from (newSDKTurnCompleter's onUsage callback).
	callAndEmit := func() {
		resp, err := loop.Completer.ChatTurn(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		estimatedTokens, err := provider.EstimatePromptCost(req.Messages, req.Tools, loop.contextAccounting())
		if err != nil {
			t.Fatal(err)
		}
		loop.emitTurnUsage(context.Background(), opts, req, resp, estimatedTokens)
	}

	// Turn 1: reported = 2*estimate. Warm start stores Ratio = 2.0, but the
	// emitted ratio must be the PRE-update value: 0.0, shown as 1.00 (unity).
	callAndEmit()
	// Turn 2: reported = estimate (raw ratio 1.0). The post-update EWMA is
	// 0.2*1.0 + 0.8*2.0 = 1.8; the emitted ratio must be the pre-update 2.0.
	loop.Completer = &usageReportingCompleter{usage: provider.TokenUsage{Reported: true, InputTokens: est, OutputTokens: 5}}
	callAndEmit()

	if len(ratios) != 2 {
		t.Fatalf("got %d token usage events, want 2", len(ratios))
	}
	if ratios[0] != 1.0 {
		t.Fatalf("turn 1 emitted ratio = %v, want 1.0 (zero-value calibrator shown as unity)", ratios[0])
	}
	if ratios[1] != 2.0 {
		t.Fatalf("turn 2 emitted ratio = %v, want 2.0 (pre-update ratio, not post-update EWMA 1.8)", ratios[1])
	}
	if !strings.Contains(details[1], "(ratio 2.00)") {
		t.Fatalf("turn 2 drift detail %q does not show the pre-update ratio", details[1])
	}
}
