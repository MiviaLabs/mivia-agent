package agent

// Regression coverage for the ToolBudget bridge (agentloop_toolbudget.go):
// proves WorkLimits.MaxToolCalls is enforced on the DEFAULT (no Backend
// override) path, which is what every production caller - including the
// live workflow-panel path (internal/workflows/controller/panel_attempt.go's
// MaxToolCalls: 64/16) - actually runs on. Before this bridge existed,
// rejectUnsupportedSDKBatches failed every such run closed on turn one with
// "agent: SDK backend does not support Options.WorkLimits.MaxToolCalls".

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestSDKDefaultBackendEnforcesCumulativeMaxToolCalls(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name})
	}
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`)},
		},
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("3", "three", `{}`)},
		},
		{Content: "done", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	// No Backend field set: this is the default path every production
	// caller (subagents.MultiStepHandler.loopOptions, and through it every
	// workflow panel member/synthesis) actually runs on.
	_, err := loop.Run(context.Background(), "run", Options{
		Model: "m", MaxSteps: 5,
		WorkLimits: appruntime.WorkLimits{MaxToolCalls: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "work limit exceeded: tool calls") {
		t.Fatalf("Run error = %v, want 'work limit exceeded: tool calls'", err)
	}
}

// TestSDKDefaultBackendMaxToolCallsWithinBudgetSucceeds is the positive
// counterpart: a run whose cumulative tool-call count stays within
// WorkLimits.MaxToolCalls completes normally on the default path.
func TestSDKDefaultBackendMaxToolCallsWithinBudgetSucceeds(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name})
	}
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`)},
		},
		{Content: "done", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "run", Options{
		Model: "m", MaxSteps: 5,
		WorkLimits: appruntime.WorkLimits{MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if text != "done" {
		t.Fatalf("text = %q, want done", text)
	}
}
