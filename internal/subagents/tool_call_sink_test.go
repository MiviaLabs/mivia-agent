package subagents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestToolCallSinkFrom_NilContextValueIsNoOp(t *testing.T) {
	if _, ok := ToolCallSinkFrom(context.Background()); ok {
		t.Fatal("expected no sink on a bare context")
	}
}

func TestContextWithToolCallSink_RoundTrips(t *testing.T) {
	var got []ToolCallStep
	sink := ToolCallSink(func(step ToolCallStep) { got = append(got, step) })
	ctx := ContextWithToolCallSink(context.Background(), sink)

	resolved, ok := ToolCallSinkFrom(ctx)
	if !ok {
		t.Fatal("expected the installed sink to resolve")
	}
	resolved(ToolCallStep{ToolCallID: "c1", Name: "read_file", Kind: "start"})
	if len(got) != 1 || got[0].ToolCallID != "c1" {
		t.Fatalf("sink did not receive the step: %+v", got)
	}
}

// TestMultiStepHandlerForwardsToolCallStepsToSink is the end-to-end
// regression: a subagent task dispatched with a ToolCallSink on its
// context must have every tool_start/tool_end event it emits ALSO
// delivered to that sink - the persistence path this session's Part B
// work is building - WITHOUT disturbing the existing live-TUI OnEvent/
// StampEventOrigin forwarding, which must fire unchanged (both fire).
func TestMultiStepHandlerForwardsToolCallStepsToSink(t *testing.T) {
	reg := newTestRegistry()
	call := provider.ToolCall{ID: "call-1", Type: "function"}
	call.Function.Name = "run_command"
	call.Function.Arguments = `{"command":"echo hi"}`
	comp := &multiStepMockCompleter{name: "test", toolCalls: []provider.ToolCall{call}, responses: []string{"done"}}

	var onEventGot []agent.Event
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
		MaxTokens:    256,
		OnEvent:      func(e agent.Event) { onEventGot = append(onEventGot, e) },
	}

	var sinkGot []ToolCallStep
	ctx := ContextWithToolCallSink(context.Background(), func(step ToolCallStep) {
		sinkGot = append(sinkGot, step)
	})

	req := runtime.Request{ID: "task-1", Name: "audit", Depth: 0, Input: json.RawMessage(`"review the patch"`)}
	if _, err := h.Invoke(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Regression pin: the existing live-TUI forwarding must be unaffected.
	if !containsKind(onEventGot, agent.EventToolStart) || !containsKind(onEventGot, agent.EventToolEnd) {
		t.Fatalf("existing OnEvent forwarding missing tool events: %+v", onEventGot)
	}

	var startSteps, endSteps int
	for _, s := range sinkGot {
		switch s.Kind {
		case "start":
			startSteps++
			if s.Name != "run_command" || s.ToolCallID != "call-1" {
				t.Fatalf("unexpected start step: %+v", s)
			}
			if s.Input == "" {
				t.Fatalf("start step missing Input: %+v", s)
			}
		case "end":
			endSteps++
			if s.Name != "run_command" || s.ToolCallID != "call-1" {
				t.Fatalf("unexpected end step: %+v", s)
			}
		default:
			t.Fatalf("unexpected step kind %q: %+v", s.Kind, s)
		}
	}
	if startSteps != 1 || endSteps != 1 {
		t.Fatalf("expected exactly one start and one end step, got start=%d end=%d (%+v)", startSteps, endSteps, sinkGot)
	}
}

func containsKind(events []agent.Event, kind agent.EventKind) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
