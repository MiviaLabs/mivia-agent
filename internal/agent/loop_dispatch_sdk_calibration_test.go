package agent

// Regression test for calibration carry on the SDK backend (session
// flip gap 1): the completer wrapper must report each Chat call's
// provider Request/Response pair back to the loop so emitTurnUsage
// updates l.Calibration, exactly as the legacy loop does per
// completion.

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// calibratingTurnCompleter answers with a TokenUsage whose input tokens
// are a per-call multiple of the request's own estimate, so the ratio
// the loop records is the multiplier (clamped by the calibrator).
type calibratingTurnCompleter struct {
	multipliers []int
	calls       int
}

func (c *calibratingTurnCompleter) Name() string { return "calibrator" }
func (c *calibratingTurnCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *calibratingTurnCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *calibratingTurnCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	i := c.calls
	if i >= len(c.multipliers) {
		i = len(c.multipliers) - 1
	}
	c.calls++
	est, _ := provider.EstimatePromptCost(req.Messages, req.Tools, provider.ContextAccountingProfile{})
	return &provider.Response{
		Content:      "ok",
		FinishReason: "stop",
		TokenUsage:   provider.TokenUsage{Reported: true, InputTokens: est * c.multipliers[i], OutputTokens: 1},
	}, nil
}

// TestSDKCompleterReportsUsageForCalibration pins the calibration
// carry: after one SDK run the loop's Calibration holds one sample;
// after a second run with a different actual-vs-estimate ratio it
// holds two samples and the ratio moved.
func TestSDKCompleterReportsUsageForCalibration(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	comp := &calibratingTurnCompleter{multipliers: []int{1, 2}}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "do work", Options{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if loop.Calibration.Samples != 1 {
		t.Fatalf("Samples = %d after first run, want 1", loop.Calibration.Samples)
	}
	ratio1 := loop.Calibration.Ratio
	if _, err := loop.Run(context.Background(), "more work", Options{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if loop.Calibration.Samples != 2 {
		t.Fatalf("Samples = %d after second run, want 2", loop.Calibration.Samples)
	}
	if loop.Calibration.Ratio == ratio1 {
		t.Fatalf("Ratio did not move after a 2x actual run: before=%f after=%f", ratio1, loop.Calibration.Ratio)
	}
}
