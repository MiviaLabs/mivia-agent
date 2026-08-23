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

func TestExecuteToolsParallel_EnforcesBatchCallBudget(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	calls := []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`), tc("3", "three", `{}`)}
	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools:   3,
		MaxToolCallsPerBatch: 2,
	})
	if len(results) != len(calls) {
		t.Fatalf("results=%d, want %d", len(results), len(calls))
	}
	if results[2].err == nil || !strings.Contains(results[2].err.Error(), "calls") {
		t.Fatalf("third result err=%v, want call budget error", results[2].err)
	}
	// The call-count budget bounds how many tools EXECUTE. It must never zero
	// or shrink the content of results that did execute - the only per-result
	// byte bound is capToolResult (MaxToolResultChars / Capability budgets).
	for i := 0; i < 2; i++ {
		if results[i].err != nil {
			t.Fatalf("result %d err=%v, want success", i, results[i].err)
		}
		if results[i].result == "" || results[i].truncated {
			t.Fatalf("result %d content=%q truncated=%v; batch ceiling must not touch executed results",
				i, results[i].result, results[i].truncated)
		}
	}
}

// TestLoopDeclinedPerBatchCallsDoNotConsumeToolBudget is the AG-BUDGET-1
// regression: step 1 returns 3 valid calls under MaxToolCallsPerBatch=2, so 2
// execute and 1 is declined with an error result; step 2 returns 2 more valid
// calls. Before the fix processToolCalls reserved len(validCalls)=3 for step 1
// even though only 2 executed, so step 2's reservation (3+2>4) aborted the run
// with "work limit exceeded: tool calls". After the fix only the 4 calls that
// actually execute are charged, and the run completes with text "done".
func TestLoopDeclinedPerBatchCallsDoNotConsumeToolBudget(t *testing.T) {
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
	text, err := loop.Run(context.Background(), "run", Options{Backend: "legacy",
		Model: "m", MaxSteps: 5,
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
	_, err := loop.Run(context.Background(), "run", Options{Backend: "legacy",
		Model: "m", MaxSteps: 5,
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

// TestExecutedToolCallCount pins the boundaries of the single rule that sizes
// a dispatched batch: unset/zero caps keep every call, at-cap keeps every
// call, and only an over-cap batch is truncated to the per-batch ceiling.
func TestExecutedToolCallCount(t *testing.T) {
	cases := []struct {
		name string
		n    int
		opts Options
		want int
	}{
		{"zero calls, cap unset", 0, Options{}, 0},
		{"under cap", 2, Options{MaxToolCallsPerBatch: 4}, 2},
		{"at cap", 4, Options{MaxToolCallsPerBatch: 4}, 4},
		{"over cap", 5, Options{MaxToolCallsPerBatch: 4}, 4},
		{"cap unset keeps all", 9, Options{}, 9},
		{"cap zero keeps all", 9, Options{MaxToolCallsPerBatch: 0}, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := executedToolCallCount(tc.n, tc.opts); got != tc.want {
				t.Fatalf("executedToolCallCount(%d, %+v) = %d, want %d", tc.n, tc.opts, got, tc.want)
			}
		})
	}
}
