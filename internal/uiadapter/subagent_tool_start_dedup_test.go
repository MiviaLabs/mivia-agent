package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestSubagentThreadListsOneToolCallPerCallID pins the fix for a subagent
// thread listing - and, once reopened, rendering - every tool call twice.
//
// The loop emits TWO EventToolStart events for ONE tool call: Detail "queued"
// from the PointPreTool hook carrying the arguments, then Detail "running"
// from the dispatcher shim carrying none, both under the same ToolCallID
// (internal/agent/sdk_tool_events.go; pinned by
// internal/agent/agentloop_maxconcurrent_test.go - 3 calls, 6 events).
// OnEventForMultiStep remaps a subagent's pair to EventSubagentStart and both
// legs reach this history, where appending blind produced a second entry with
// null arguments and no output.
//
// The pair below is the exact wire shape a real one-tool subagent run emits.
func TestSubagentThreadListsOneToolCallPerCallID(t *testing.T) {
	threads := NewSubagentThreads()
	origin := agent.EventOrigin{TaskID: "task-1", Agent: "general-purpose"}

	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, ToolCallID: "call_1",
		Name: "read_file", Detail: "queued", Input: `{"path":"a.go"}`,
	}, TranslateOptions{})
	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, ToolCallID: "call_1",
		Name: "read_file", Detail: "running",
	}, TranslateOptions{})
	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentEnd, Origin: origin, ToolCallID: "call_1",
		Name: "read_file", Detail: "completed", Output: "package main",
	}, TranslateOptions{})

	conv, ok := threads.Thread("task-1")
	if !ok {
		t.Fatal("no thread registered for task-1")
	}
	var calls int
	for _, m := range conv.History() {
		calls += len(m.ToolCalls)
	}
	if calls != 1 {
		t.Fatalf("thread lists %d tool calls, want 1 (one call, two tool_start legs)", calls)
	}

	tc := conv.History()[0].ToolCalls[0]
	if tc.Arguments != `{"path":"a.go"}` {
		t.Errorf("Arguments = %q, want the queued leg's args (the running leg carries none)", tc.Arguments)
	}
	if tc.Output != "package main" {
		t.Errorf("Output = %q, want the tool_end result matched onto the surviving row", tc.Output)
	}
}

// TestSubagentThreadKeepsUnidentifiedToolStarts pins the dedupe's boundary: a
// tool_start with no ToolCallID cannot be matched to a sibling leg, so it is
// listed rather than folded into an unrelated call.
func TestSubagentThreadKeepsUnidentifiedToolStarts(t *testing.T) {
	threads := NewSubagentThreads()
	origin := agent.EventOrigin{TaskID: "task-2", Agent: "general-purpose"}

	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, Name: "grep", Detail: "queued",
	}, TranslateOptions{})
	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, Name: "read_file", Detail: "queued",
	}, TranslateOptions{})

	conv, ok := threads.Thread("task-2")
	if !ok {
		t.Fatal("no thread registered for task-2")
	}
	var calls int
	for _, m := range conv.History() {
		calls += len(m.ToolCalls)
	}
	if calls != 2 {
		t.Fatalf("thread lists %d tool calls, want 2 (no id to dedupe on)", calls)
	}
}

// TestASecondLegCarryingArgumentsFillsTheRow pins the merge's other
// direction. The pair usually arrives args-first ("queued" carries them,
// "running" does not), and the merge is written to fill only fields a leg
// actually carries - so a pair that arrives in the other order, or a
// first leg admitted before its arguments were resolved, must still end
// with the arguments on the row rather than an empty string.
//
// Without this the merge would look correct on the common ordering and
// silently drop arguments on the other one.
func TestASecondLegCarryingArgumentsFillsTheRow(t *testing.T) {
	threads := NewSubagentThreads()
	origin := agent.EventOrigin{TaskID: "task-2", Agent: "general-purpose"}

	// First leg: identified, but with no arguments yet.
	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, ToolCallID: "call_9",
		Name: "read_file", Detail: "queued",
	}, TranslateOptions{})
	// Second leg carries them.
	threads.HandleEvent(agent.Event{
		Kind: agent.EventSubagentStart, Origin: origin, ToolCallID: "call_9",
		Name: "read_file", Detail: "running", Input: `{"path":"b.go"}`,
	}, TranslateOptions{})

	conv, ok := threads.Thread("task-2")
	if !ok {
		t.Fatal("no thread registered for task-2")
	}
	var calls []string
	var args string
	for _, m := range conv.History() {
		for _, tc := range m.ToolCalls {
			calls = append(calls, tc.ID)
			args = tc.Arguments
		}
	}
	if len(calls) != 1 {
		t.Fatalf("thread lists %d tool calls, want 1: %v", len(calls), calls)
	}
	if args != `{"path":"b.go"}` {
		t.Errorf("Arguments = %q, want the second leg's args merged onto the row", args)
	}
}
