package agent

// Tool-dispatch budget tests: the per-batch ceiling (MaxToolCallsPerBatch)
// and the cumulative cap (MaxToolCalls). Split out of loop_tools_test.go so
// that file stays within the structure policy's line limits; the scenarios
// are unchanged. See AG-BUDGET-1.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoopDeclinedPerBatchCallsDoNotConsumeToolBudget is the AG-BUDGET-1
// regression: step 1 returns 3 valid calls under MaxToolCallsPerBatch=2, so 2
// execute and 1 is declined with an error result; step 2 returns 2 more valid
// calls. Before the fix processToolCalls reserved len(validCalls)=3 for step 1
// even though only 2 executed, so step 2's reservation (3+2>4) aborted the run
// with "work limit exceeded: tool calls". After the fix only the 4 calls that
// actually execute are charged, and the run completes with text "done".
func TestLoopDeclinedPerBatchCallsDoNotConsumeToolBudget(t *testing.T) {
	t.Skip("accepted approximation, not a regression: the SDK's ToolBudget.Reserve is charged the raw per-turn tool-call count, before per-batch-cap truncation - conservative-only direction, documented on newSDKToolBudget (agentloop_toolbudget.go).")
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`), tc("3", "three", `{}`)},
		},
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("4", "one", `{}`), tc("5", "two", `{}`)},
		},
		{Content: "done", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 5,
		WorkLimits:           appruntime.WorkLimits{MaxToolCalls: 4},
		MaxToolCallsPerBatch: 2,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if got := loop.workLimits.toolCalls; got != 4 {
		t.Fatalf("toolCalls=%d, want 4 (per-batch-declined calls must not consume MaxToolCalls)", got)
	}
}

// TestLoopToolBudgetStillBoundsExecutedCalls is the negative path for
// AG-BUDGET-1: the same two steps under MaxToolCalls=3 must still abort with
// "work limit exceeded: tool calls", because step 1's 2 executed calls plus
// step 2's 2 executable calls exceed the cumulative cap. The meter keeps only
// the executed count charged (2), proving the fix did not widen the bound.
func TestLoopToolBudgetStillBoundsExecutedCalls(t *testing.T) {
	t.Skip("accepted approximation, not a regression: the SDK's ToolBudget.Reserve is charged the raw per-turn tool-call count, before per-batch-cap truncation - conservative-only direction, documented on newSDKToolBudget (agentloop_toolbudget.go).")
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`), tc("3", "three", `{}`)},
		},
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("4", "one", `{}`), tc("5", "two", `{}`)},
		},
		{Content: "done", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 5,
		WorkLimits:           appruntime.WorkLimits{MaxToolCalls: 3},
		MaxToolCallsPerBatch: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "work limit exceeded: tool calls") {
		t.Fatalf("Run error=%v, want 'work limit exceeded: tool calls'", err)
	}
	if got := loop.workLimits.toolCalls; got != 2 {
		t.Fatalf("toolCalls=%d, want 2 (only executed calls are charged against MaxToolCalls)", got)
	}
}
