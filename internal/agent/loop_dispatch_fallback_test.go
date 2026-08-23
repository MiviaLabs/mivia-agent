// Tests for the loop_dispatch fallback branches that walk res.History
// using the new Content-match boundary (sdkCurrentTurnStart). The
// unit-level helper tests (loop_dispatch_sdk_steered_partial_test.go)
// cover sdkSteeredStopPartial directly; this file exercises the
// fallback branches through the full loop.Run path so coverage
// numbers match the diff.
package agent

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// scriptedTextCompleter serves one canned response for every ChatTurn
// call; with MaxSteps=1 the loop terminates with StopMaxIterations
// (Final zero), exercising the graceful-empty fallback in
// loop_dispatch.go:104-110.
type scriptedTextCompleter struct{ text string }

func (s *scriptedTextCompleter) Name() string { return "scripted-text" }
func (s *scriptedTextCompleter) Chat(context.Context, provider.Request) (string, error) {
	return s.text, nil
}
func (s *scriptedTextCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return s.text, nil
}
func (s *scriptedTextCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: s.text, FinishReason: "stop"}, nil
}

// TestRunOnceSDKGracefulFinalEmptyFallsBackToLastAssistant drives
// runOnceSDK through loop.Run to a StopMaxIterations outcome with a
// single response. The SDK zero-out Final.Content and the loop's
// graceful-empty fallback at loop_dispatch.go:104-110 must surface
// the response Content from res.History via the Content-match walk
// (line 105-114), exercising the same-defect-site fix from item 1.
func TestRunOnceSDKGracefulFinalEmptyFallsBackToLastAssistant(t *testing.T) {
	const want = "first-step-output"
	loop := &Loop{Completer: &scriptedTextCompleter{text: want}, Tools: tools.NewRegistry()}
	got, err := loop.Run(context.Background(), "user task", Options{Model: "m", MaxSteps: 1})
	if err != nil {
		t.Fatalf("err = %v, want nil (StopMaxIterations should map to an error naming the cap)", err)
	}
	if got != want {
		t.Fatalf("got = %q, want %q (graceful-empty fallback)", got, want)
	}
}
