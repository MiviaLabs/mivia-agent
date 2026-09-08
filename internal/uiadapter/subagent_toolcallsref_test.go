package uiadapter

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestMatchTaskToolCallsRefs_IDMatch pins the primary pairing rule: a
// result's ToolCallsRef travels to the task sharing its TaskID, regardless
// of slice order.
func TestMatchTaskToolCallsRefs_IDMatch(t *testing.T) {
	results := []encodedTaskResult{
		{TaskID: "task-b", ToolCallsRef: "ref-b"},
		{TaskID: "task-a", ToolCallsRef: ""},
	}
	tasks := []parsedDispatchTask{
		{ID: "task-a"},
		{ID: "task-b"},
	}
	got := matchTaskToolCallsRefs(results, tasks)
	want := []string{"", "ref-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestMatchTaskToolCallsRefs_PositionalFallbackWhenIDAbsent pins the
// fallback: when a result carries no TaskID at all but the counts agree,
// pairing falls back to position.
func TestMatchTaskToolCallsRefs_PositionalFallbackWhenIDAbsent(t *testing.T) {
	results := []encodedTaskResult{
		{ToolCallsRef: "ref-1"},
		{ToolCallsRef: "ref-2"},
	}
	tasks := []parsedDispatchTask{
		{ID: "task-a"},
		{ID: "task-b"},
	}
	got := matchTaskToolCallsRefs(results, tasks)
	want := []string{"ref-1", "ref-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestMatchTaskToolCallsRefs_MismatchedCountsNoFallback pins that the
// positional fallback is ONLY applied when len(results) == len(tasks):
// with mismatched counts and no ID match, every task gets an empty ref
// rather than a misaligned guess.
func TestMatchTaskToolCallsRefs_MismatchedCountsNoFallback(t *testing.T) {
	results := []encodedTaskResult{
		{ToolCallsRef: "ref-1"},
	}
	tasks := []parsedDispatchTask{
		{ID: "task-a"},
		{ID: "task-b"},
	}
	got := matchTaskToolCallsRefs(results, tasks)
	want := []string{"", ""}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestMatchTaskToolCallsRefs_ZeroResultsYieldAllEmpty pins the whole-run
// failure case: unlike matchTaskOutputs, there is no rawErrorEnvelopeText-
// style fallback here - a tool_calls_ref never appears in a run-level
// failure envelope, so zero results must yield an empty string per task,
// never any parsed error text.
func TestMatchTaskToolCallsRefs_ZeroResultsYieldAllEmpty(t *testing.T) {
	tasks := []parsedDispatchTask{
		{ID: "task-a"},
		{ID: "task-b"},
	}
	got := matchTaskToolCallsRefs(nil, tasks)
	want := []string{"", ""}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestPopulateDispatchTasks_ThreadsToolCallsRefIntoConversation is the
// end-to-end proof for this slice: a dispatch_tasks result whose
// tool_calls_ref is set (and no inline tool_calls) must land on the
// reconstructed *SubagentTranscriptConversation's new pending-ref field,
// with NO resolution happening - History() must render exactly the same
// "(tool calls recorded)" notice it does today, since nothing consumes the
// field yet in this slice.
func TestPopulateDispatchTasks_ThreadsToolCallsRefIntoConversation(t *testing.T) {
	threads := NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_refonly",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-refonly","prompt":"tool-only work","agent":"worker"}]}`,
					Output:    `[{"task_id":"task-refonly","status":"completed","tool_calls_ref":"ref:tool_calls:abc123def456"}]`,
				},
			},
		},
	}

	PopulateFromToolCalls(threads, msgs)

	got, ok := threads.Thread("call_dispatch_refonly:task-refonly")
	if !ok || got == nil {
		t.Fatal("expected thread for task-refonly")
	}
	conv, isTranscript := got.(*SubagentTranscriptConversation)
	if !isTranscript {
		t.Fatalf("expected *SubagentTranscriptConversation, got %T", got)
	}

	conv.mu.Lock()
	ref := conv.sourceToolCallsRef
	conv.mu.Unlock()
	if ref != "ref:tool_calls:abc123def456" {
		t.Errorf("sourceToolCallsRef = %q, want %q", ref, "ref:tool_calls:abc123def456")
	}

	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history messages (prompt + notice), got %d: %+v", len(hist), hist)
	}
	if hist[1].Text != "(tool calls recorded)" {
		t.Errorf("History() notice changed: got %q, want %q - this slice must not alter behavior", hist[1].Text, "(tool calls recorded)")
	}
}
