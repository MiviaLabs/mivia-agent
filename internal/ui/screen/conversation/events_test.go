package conversation

import (
	"strings"
	"testing"
)

// TestDispatchTaskIDsMissingIDFallbackIsFriendly pins the fix for a raw
// provider tool_call_id leaking into a visible sidebar row: a task the
// model forgot to give an "id" used to fall back to "{callID}-{index}",
// so the row's ID (and therefore its rendered label - panelAgentRow shows
// IDs directly) exposed the raw call id verbatim, e.g.
// "call_95bcae0ca204bc76-1". The fallback must never embed callID.
func TestDispatchTaskIDsMissingIDFallbackIsFriendly(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "quality", "prompt": "check quality"},
			map[string]any{"prompt": "no id supplied"},
		},
	}
	got := dispatchTaskIDs("call_95bcae0ca204bc76", "dispatch_tasks", args)
	if len(got) != 2 {
		t.Fatalf("got %d ids, want 2: %v", len(got), got)
	}
	if got[0] != "quality" {
		t.Errorf("id[0] = %q, want the model-supplied %q", got[0], "quality")
	}
	if strings.Contains(got[1], "call_95bcae0ca204bc76") {
		t.Errorf("id[1] = %q, must not embed the raw provider call id", got[1])
	}
	if got[1] != "task-2" {
		t.Errorf("id[1] = %q, want a friendly fallback (task-2)", got[1])
	}
}

// TestDispatchTaskIDsUnrelatedToolReturnsNil pins the existing guard: a
// non-dispatch_tasks tool name (or missing task list) leaves the caller's
// single-row behavior unchanged.
func TestDispatchTaskIDsUnrelatedToolReturnsNil(t *testing.T) {
	if got := dispatchTaskIDs("call-1", "delegate", map[string]any{}); got != nil {
		t.Errorf("got %v, want nil for a non-dispatch_tasks tool", got)
	}
}
