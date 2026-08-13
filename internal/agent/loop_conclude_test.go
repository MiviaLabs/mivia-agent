package agent

// Soft conclude tests: when a work bound (deadline, output budget, or
// tool-call budget) is close, stepRequest injects the bounded conclude
// instruction into an EPHEMERAL copy of the request messages — never into
// l.Messages — so the model can wrap up with its best valid result instead of
// the bound hard-aborting the run mid-work. The injected message is
// host-generated, so these tests assert request contents, not model behavior.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func lastMessageContent(req provider.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Content
}

// TestConcludeSteerInjectedWhenToolCallsNearlyExhausted: with MaxToolCalls=4,
// step 1 executes 2 calls (2 remain), so step 2's request must carry the
// conclude instruction and step 1's must not. The mock ignores the message,
// the remaining calls execute, and the run completes — the hard "work limit
// exceeded: tool calls" abort is preempted by the soft conclude.
func TestConcludeSteerInjectedWhenToolCallsNearlyExhausted(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	comp := &steerCompleter{steps: []steerStep{
		{resp: provider.Response{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`)}}},
		{resp: provider.Response{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("3", "one", `{}`), tc("4", "two", `{}`)}}},
		{resp: provider.Response{Content: "done", FinishReason: "stop"}},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "run", Options{
		Model: "m", MaxSteps: 5,
		WorkLimits: appruntime.WorkLimits{MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) < 3 {
		t.Fatalf("requests=%d, want >=3", len(comp.requests))
	}
	if got := lastMessageContent(comp.requests[0]); got == concludeMessage {
		t.Fatalf("step 1 request carried the conclude message with a full tool budget: %q", got)
	}
	for i := 1; i < len(comp.requests); i++ {
		if got := lastMessageContent(comp.requests[i]); got != concludeMessage {
			t.Fatalf("request %d last message = %q, want the conclude message", i, got)
		}
	}
	// The conclude message is host-injected: it must never land in history.
	for _, m := range loop.Messages {
		if strings.Contains(m.Content, "Work-limit notice") {
			t.Fatalf("conclude instruction leaked into l.Messages history: %q", m.Content)
		}
	}
}

// TestConcludeSteerInjectedWhenDeadlineClose: a ctx deadline under
// concludeTimeThreshold makes the request carry the conclude instruction, and
// the loop emits EventWorkLimit for observability.
func TestConcludeSteerInjectedWhenDeadlineClose(t *testing.T) {
	comp := &steerCompleter{}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	var sawWorkLimit bool
	text, err := loop.Run(ctx, "run", Options{
		Model: "m", MaxSteps: 5,
		OnEvent: func(e Event) {
			if e.Kind == EventWorkLimit {
				sawWorkLimit = true
			}
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) == 0 {
		t.Fatal("no provider request recorded")
	}
	if got := lastMessageContent(comp.requests[0]); got != concludeMessage {
		t.Fatalf("request last message = %q, want the conclude message", got)
	}
	if !sawWorkLimit {
		t.Fatal("expected an EventWorkLimit event when the conclude instruction fired")
	}
}

// TestConcludeSteerNotInjectedWithoutBounds: no work limits and no deadline
// must leave the request untouched (the last message stays the user text).
func TestConcludeSteerNotInjectedWithoutBounds(t *testing.T) {
	comp := &steerCompleter{}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	text, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) == 0 {
		t.Fatal("no provider request recorded")
	}
	if got := lastMessageContent(comp.requests[0]); got != "run" {
		t.Fatalf("request last message = %q, want the user text (no conclude injection)", got)
	}
}

// TestConcludeSteerInjectedWhenOutputBudgetNearlyExhausted: fewer than one
// full per-call allowance of cumulative output budget left (4000 < 8192) fires
// the conclude instruction on the next request.
func TestConcludeSteerInjectedWhenOutputBudgetNearlyExhausted(t *testing.T) {
	comp := &steerCompleter{}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	text, err := loop.Run(context.Background(), "run", Options{
		Model: "m", MaxSteps: 5,
		WorkLimits: appruntime.WorkLimits{MaxOutputTokens: 4000, MaxOutputPerCall: 8192},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) == 0 {
		t.Fatal("no provider request recorded")
	}
	if got := lastMessageContent(comp.requests[0]); got != concludeMessage {
		t.Fatalf("request last message = %q, want the conclude message", got)
	}
}

// TestConcludeSteerBoundaryFullPerCallAllowance pins the output-budget
// boundary: a full per-call allowance remaining (8192 of 8192) must NOT fire
// the conclude instruction.
func TestConcludeSteerBoundaryFullPerCallAllowance(t *testing.T) {
	comp := &steerCompleter{}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	text, err := loop.Run(context.Background(), "run", Options{
		Model: "m", MaxSteps: 5,
		WorkLimits: appruntime.WorkLimits{MaxOutputTokens: 8192, MaxOutputPerCall: 8192},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) == 0 {
		t.Fatal("no provider request recorded")
	}
	if got := lastMessageContent(comp.requests[0]); got == concludeMessage {
		t.Fatalf("conclude fired with a full per-call allowance remaining: %q", got)
	}
}
