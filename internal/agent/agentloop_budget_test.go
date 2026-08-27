package agent

// Regression tests for Item 8: the SDK backend path reserves and
// refunds output-token budget through the SAME workLimitMeter methods
// the legacy loop uses (reserveProvider/refundProvider/outputCap), so
// a shared MaxOutputTokens ceiling decrements on reserve, refunds the
// unused output reservation after the real Usage arrives, and fails
// closed before the provider call when the reservation exceeds the
// ceiling.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestSDKBackendReservesAndRefundsSharedCeiling pins the Item 8
// contract: with MaxTokens=50 reserved per step against a shared
// MaxOutputTokens=60 ceiling and a completion that reports 2 output
// tokens, each run must refund the unused 48 so a SECOND run on the
// same preserved meter still fits. Without the refund the second run's
// 50-token reservation exceeds the 10 remaining and hard-fails.
func TestSDKBackendReservesAndRefundsSharedCeiling(t *testing.T) {
	maxTokens := 50
	step := provider.Response{
		Content:      "answer",
		FinishReason: "stop",
		TokenUsage:   provider.TokenUsage{Reported: true, InputTokens: 10, OutputTokens: 2},
	}
	comp := &scriptedTurnCompleter{steps: []provider.Response{step, step}}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:              "m",
		MaxSteps:           3,
		MaxTokens:          &maxTokens,
		PreserveWorkLimits: true,
		WorkLimits:         runtime.WorkLimits{MaxOutputTokens: 60},
	}
	if _, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q1"}}); err != nil {
		t.Fatalf("run 1 err = %v, want nil", err)
	}
	if got := loop.workLimits.outputTokens; got != 2 {
		t.Fatalf("after run 1 outputTokens = %d, want 2 (50 reserved, 48 refunded)", got)
	}
	if _, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q2"}}); err != nil {
		t.Fatalf("run 2 err = %v, want nil (refund must return the unused reservation)", err)
	}
	if got := loop.workLimits.outputTokens; got != 4 {
		t.Fatalf("after run 2 outputTokens = %d, want 4", got)
	}
}

// TestSDKBackendWorkLimitFailsClosedBeforeCall pins the fail-closed
// rule: a MaxPromptTokens ceiling the estimated prompt exceeds makes
// the SDK run fail with the legacy "work limit exceeded" error BEFORE
// any provider call runs.
func TestSDKBackendWorkLimitFailsClosedBeforeCall(t *testing.T) {
	comp := &scriptedTurnCompleter{steps: []provider.Response{{Content: "no", FinishReason: "stop"}}}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:      "m",
		MaxSteps:   3,
		WorkLimits: runtime.WorkLimits{MaxPromptTokens: 1},
	}
	_, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "a prompt far over one token"}})
	if err == nil || !strings.Contains(err.Error(), "work limit exceeded: prompt tokens") {
		t.Fatalf("err = %v, want work limit exceeded: prompt tokens", err)
	}
	if comp.calls != 0 {
		t.Fatalf("completer calls = %d, want 0 (reservation fails before the call)", comp.calls)
	}
}
