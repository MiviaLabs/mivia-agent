package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestSubagentDoneProgressClosesOneDispatchedRowLive is the end-to-end
// regression for the "sidebar waits for every subagent before updating"
// bug: a dispatch_tasks call blocks until ALL dispatched tasks finish, so
// its own tool.end (and the group-status resolution
// TestDispatchTasksToolStartFansOutPanelRowsPerTask exercises) only ever
// arrives once, after the slowest task. Before uiadapter's
// translateSubagentDone carried a per-task tool.output progress update
// (see event_kind.go), a task's row had no other way to leave "running"
// early - it sat pinned until every sibling task also finished. This
// pins that a single early-finishing task's row updates on its own,
// independent of the other three still running and of the outer call.
func TestSubagentDoneProgressClosesOneDispatchedRowLive(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "dispatch_tasks-call-1",
			Name:       "dispatch_tasks",
			Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "task-a", "prompt": "a"},
					map[string]any{"id": "task-b", "prompt": "b"},
					map[string]any{"id": "task-c", "prompt": "c"},
					map[string]any{"id": "task-d", "prompt": "d"},
				},
			},
		},
	}})
	got := next.(Screen)

	// task-a's own subagent run finishes (translateSubagentDone's live
	// signal) long before dispatch_tasks itself returns.
	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "dispatch_tasks-call-1:task-a",
			Progress:   &uievent.Progress{Status: "completed"},
		},
	}})
	got = next.(Screen)

	statuses := map[string]string{}
	for _, a := range got.panel.agents {
		statuses[a.ID] = a.Status
	}
	if statuses["dispatch_tasks-call-1:task-a"] != "completed" {
		t.Errorf("task-a status = %q, want completed (live, before dispatch_tasks itself ends)", statuses["dispatch_tasks-call-1:task-a"])
	}
	for _, id := range []string{"task-b", "task-c", "task-d"} {
		id = "dispatch_tasks-call-1:" + id
		if statuses[id] != "running" {
			t.Errorf("%s status = %q, want running (unaffected by task-a's own completion)", id, statuses[id])
		}
	}
	if n := got.panel.activeAgentCount(); n != 3 {
		t.Errorf("activeAgentCount = %d, want 3 (task-a no longer counted, dispatch_tasks itself has not ended)", n)
	}
}
