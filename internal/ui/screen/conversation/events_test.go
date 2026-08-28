package conversation

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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
	// Namespaced with the call id (matching what a live dispatch_tasks call
	// actually mints - internal/cliorchestrate/dispatch.go's
	// dispatchNamespace), not the model-supplied id verbatim.
	if want := "call_95bcae0ca204bc76:quality"; got[0] != want {
		t.Errorf("id[0] = %q, want %q", got[0], want)
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

// TestObserveToolStart_DuplicateEmissionDoesNotStrayFromTheGroup pins a
// real user-reported bug: a raw provider call id (e.g.
// "call_b8875f23faac7c36") showed up as its own stray sidebar row
// alongside a dispatch_tasks batch's correctly-named per-task rows.
//
// Root cause: the agent loop emits TWO tool.start events per dispatch -
// "queued" (internal/agent/sdk_tool_events.go, Args populated via
// redactToolInputForTool) and then "running" (internal/agent/
// sdk_dispatcher_shim.go's dispatcherShim.Run, which never sets Input at
// all). Both translate to uievent.KindToolStart and both reach
// observeToolStart. The first correctly fans out via dispatchTaskIDs; the
// second carries nil Args (parseArgs("") returns nil), dispatchTaskIDs
// fails, and observeToolStart's fallback creates a NEW single row keyed by
// the raw ToolCallID - right alongside the already-fanned-out rows.
//
// A duplicate tool.start for a call already registered as a dispatch
// group must be a no-op, not a second, differently-shaped registration
// attempt.
func TestObserveToolStart_DuplicateEmissionDoesNotStrayFromTheGroup(t *testing.T) {
	var s Screen
	queued := uievent.ToolStartBody{
		ToolCallID: "call_b8875f23faac7c36",
		Name:       "dispatch_tasks",
		Args: map[string]any{
			"tasks": []any{
				map[string]any{"id": "explore-a", "prompt": "look at a"},
				map[string]any{"id": "explore-b", "prompt": "look at b"},
			},
		},
	}
	s.observeToolStart(queued)

	running := uievent.ToolStartBody{
		ToolCallID: "call_b8875f23faac7c36",
		Name:       "dispatch_tasks",
		Args:       nil,
	}
	s.observeToolStart(running)

	if len(s.panel.agents) != 2 {
		t.Fatalf("got %d agent rows, want 2 (explore-a, explore-b): %+v", len(s.panel.agents), s.panel.agents)
	}
	// Each row is legitimately namespaced with the call id as a PREFIX
	// (dispatchTaskIDs/namespacedTaskID) - the bug this test guards is a
	// STRAY row keyed on the bare call id alone (the second "running"
	// tool.start re-deriving ids from nil Args and falling back to a
	// single row for the whole call), not the prefix itself.
	for _, a := range s.panel.agents {
		if a.ID == "call_b8875f23faac7c36" {
			t.Errorf("stray row for the raw call id leaked into the panel: %+v", a)
		}
	}
}
