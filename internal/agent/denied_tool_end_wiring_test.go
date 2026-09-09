package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// A refused tool call must reach the operator's surfaces AS refused.
//
// The refusal travels by a join: the approval wrapper reports it, the adapter
// records an outcome, and the loop's emitter derives the tool_end detail from
// that outcome. Every piece had a test and the JOIN had none, so flipping the
// recorded outcome's failed flag reinstated the original defect - a refused
// call reported as a success on every surface - with this package green.
//
// This test drives the whole loop and asserts on the event a viewer actually
// classifies.

// deniedEndEvents runs one turn whose single tool call is refused by policy,
// and returns the tool_end events it emitted plus whether the tool ran.
func deniedEndEvents(t *testing.T, policy string) ([]Event, bool) {
	t.Helper()

	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "write_tool", class: tools.ExecutionWrite, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "write_tool", `{}`)},
			},
			{Content: "done", FinishReason: "stop"},
		},
	}

	var mu struct {
		events []Event
	}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "go", Options{
		Model:          "test-model",
		MaxSteps:       3,
		ApprovalPolicy: policy,
		OnEvent: func(e Event) {
			if e.Kind == EventToolEnd {
				mu.events = append(mu.events, e)
			}
		},
	}); err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	return mu.events, started.Load() > 0
}

// TestADeniedCallEmitsAFailedToolEnd is the join.
func TestADeniedCallEmitsAFailedToolEnd(t *testing.T) {
	ends, ran := deniedEndEvents(t, "deny")

	if ran {
		t.Fatal("the tool ran under a deny policy")
	}
	if len(ends) == 0 {
		t.Fatal("a refused call emitted no tool_end at all, so a viewer is left " +
			"with a tool that started and never finished")
	}
	for _, e := range ends {
		if strings.Contains(e.Detail, "duplicate") {
			t.Errorf("detail = %q: a refused call is being reported through the "+
				"dedup fallback, which every surface classifies as success", e.Detail)
		}
		if !strings.HasPrefix(e.Detail, "failed") {
			t.Errorf("detail = %q, want a failed prefix - both the NDJSON status "+
				"mapping and the TUI's OK flag key on it, so without it the "+
				"operator is told a call they refused succeeded", e.Detail)
		}
	}
}

// TestAnApprovedCallStillEmitsASuccessfulToolEnd is the other direction: the
// failed prefix must be specific to a refusal, or every ordinary call would be
// reported as an error.
func TestAnApprovedCallStillEmitsASuccessfulToolEnd(t *testing.T) {
	ends, ran := deniedEndEvents(t, "auto")

	if !ran {
		t.Fatal("the tool did not run under an auto policy")
	}
	if len(ends) == 0 {
		t.Fatal("an approved call emitted no tool_end")
	}
	for _, e := range ends {
		if strings.HasPrefix(e.Detail, "failed") {
			t.Errorf("detail = %q: an approved, successful call was reported as a "+
				"failure", e.Detail)
		}
	}
}
