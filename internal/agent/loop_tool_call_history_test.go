package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The loop's own history is the input to strict context planning
// (provider.ValidateToolPairing via contextmgr.validateMessageShape), which
// repairs nothing and fails the whole turn on any shape the API would reject.
// Every step must therefore leave history that validates as-is: a recorded
// tool result needs its announced call, and a recorded call needs arguments
// that are a JSON object.

func TestMalformedToolCallStaysPairedInHistory(t *testing.T) {
	started := &atomic.Int32{}
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a", started: started})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "read_file", `{"path":`)}},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "read malformed", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history a malformed tool call cannot be planned: %v", err)
	}
	if started.Load() != 0 {
		t.Fatalf("malformed call was dispatched to the registry %d times", started.Load())
	}
	call, ok := announcedCall(loop.Messages, "1")
	if !ok {
		t.Fatalf("malformed call was not announced; messages: %+v", loop.Messages)
	}
	if call.Function.Arguments != "{}" {
		t.Fatalf("announced arguments = %q, want %q", call.Function.Arguments, "{}")
	}
	result, ok := toolResult(loop.Messages, "1")
	if !ok {
		t.Fatal("malformed call has no paired error result")
	}
	if result.Content == "" {
		t.Fatal("malformed call error result is empty")
	}
}

func TestArgumentlessToolCallRecordsAJSONObject(t *testing.T) {
	started := &atomic.Int32{}
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a", started: started})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "read_file", "")}},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "call with no arguments", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history of an argumentless tool call cannot be planned: %v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("argumentless call executions = %d, want 1", started.Load())
	}
	call, ok := announcedCall(loop.Messages, "1")
	if !ok {
		t.Fatalf("argumentless call was not announced; messages: %+v", loop.Messages)
	}
	if call.Function.Arguments != "{}" {
		t.Fatalf("announced arguments = %q, want %q", call.Function.Arguments, "{}")
	}
}

func TestMalformedCallBesideAValidOneIsNotOrphaned(t *testing.T) {
	started := &atomic.Int32{}
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a", started: started})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{
				tc("1", "read_file", `{"path":"a"}`),
				tc("2", "read_file", `{"path":`),
			}},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "mixed batch", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	// The reported failure: the skipped call's error result referenced a
	// tool_call_id no assistant message announced.
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history of a mixed batch cannot be planned: %v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1 (only the well-formed call)", started.Load())
	}
}

func TestAllMalformedToolCallsLeaveAValidAssistantMessage(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "read_file", "not json")}},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "malformed only", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	// An assistant message with neither content nor tool calls is rejected by
	// the same validator, so a step whose every call was malformed must still
	// announce them.
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history of an all-malformed step cannot be planned: %v", err)
	}
}

func TestUnidentifiedToolCallIsGivenAnIDAndStaysPaired(t *testing.T) {
	started := &atomic.Int32{}
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a", started: started})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{
				tc("", "read_file", `{"path":"a"}`),
				tc("", "read_file", `{"path":`),
			}},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "unidentified calls", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	// A call the provider left unidentified cannot be paired with its result by
	// id, so the loop assigns one before recording or dispatching anything.
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history of an unidentified tool call cannot be planned: %v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1 (only the well-formed call)", started.Load())
	}
	ids := map[string]bool{}
	for _, message := range loop.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				t.Fatal("a recorded tool call still has no ID")
			}
			if ids[call.ID] {
				t.Fatalf("tool call ID %q was assigned twice in one batch", call.ID)
			}
			ids[call.ID] = true
		}
	}
	if len(ids) != 2 {
		t.Fatalf("announced calls = %d, want 2", len(ids))
	}
}

func TestSynthesizedToolCallIDsDoNotRepeatAcrossSteps(t *testing.T) {
	// ValidateToolPairing rejects a reused tool call ID across the whole
	// history, so a per-turn counter would fail on the second unidentified call.
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	step := provider.Response{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("", "read_file", `{"path":"a"}`)}}
	comp := &scriptCompleter{steps: []provider.Response{step, step}}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "two unidentified steps", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("history of repeated unidentified calls cannot be planned: %v", err)
	}
}

func announcedCall(messages []provider.Message, id string) (provider.ToolCall, bool) {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID == id {
				return call, true
			}
		}
	}
	return provider.ToolCall{}, false
}

func toolResult(messages []provider.Message, id string) (provider.Message, bool) {
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.ToolCallID == id {
			return message, true
		}
	}
	return provider.Message{}, false
}
