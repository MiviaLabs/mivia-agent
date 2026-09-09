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

// TestConcurrentDispatchedRowsUpdateIndependentlyLive is the multi-row
// analogue of TestSubagentDoneProgressClosesOneDispatchedRowLive: three
// dispatched tasks running concurrently each emit their own heartbeat-derived
// tool.output progress, interleaved rather than in row order, and each row
// must reflect only its own task's latest step/toolcall/log counters. A
// heartbeat for one task must never bleed into a sibling's row.
func TestConcurrentDispatchedRowsUpdateIndependentlyLive(t *testing.T) {
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
				},
			},
		},
	}})
	got := next.(Screen)

	sendProgress := func(id string, step, toolCalls int, log string) {
		next, _ := got.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{
				ToolCallID: "dispatch_tasks-call-1:" + id,
				Progress: &uievent.Progress{
					Status:    "running",
					Step:      step,
					ToolCalls: toolCalls,
					Log:       []string{log},
				},
			},
		}})
		got = next.(Screen)
	}

	// Interleaved as they'd arrive concurrently in real time: not grouped
	// by task, and not in ascending step order across tasks.
	sendProgress("task-b", 1, 1, "b-step1")
	sendProgress("task-a", 1, 2, "a-step1")
	sendProgress("task-c", 1, 1, "c-step1")
	sendProgress("task-a", 2, 3, "a-step2")
	sendProgress("task-b", 2, 2, "b-step2")
	sendProgress("task-c", 2, 3, "c-step2")

	byID := map[string]subagentRow{}
	for _, a := range got.panel.agents {
		byID[a.ID] = a
	}

	want := map[string]struct {
		step, toolCalls int
		logLen          int
	}{
		"dispatch_tasks-call-1:task-a": {2, 3, 2},
		"dispatch_tasks-call-1:task-b": {2, 2, 2},
		"dispatch_tasks-call-1:task-c": {2, 3, 2},
	}
	for id, w := range want {
		row, ok := byID[id]
		if !ok {
			t.Fatalf("row %s not found among %d rows", id, len(got.panel.agents))
		}
		if row.Step != w.step {
			t.Errorf("%s Step = %d, want %d", id, row.Step, w.step)
		}
		if row.ToolCalls != w.toolCalls {
			t.Errorf("%s ToolCalls = %d, want %d", id, row.ToolCalls, w.toolCalls)
		}
		if len(row.Log) != w.logLen {
			t.Errorf("%s Log = %v, want %d entries", id, row.Log, w.logLen)
		}
	}
	if len(got.panel.agents) != 3 {
		t.Errorf("agents = %d rows, want exactly 3 (no orphan rows created by cross-task attribution)", len(got.panel.agents))
	}
}

// TestObserveAgent_DoubleNamespacedSameSuffixUpdatesExistingRow reproduces
// the mismatch flagged for a task whose progress event's Origin.TaskID
// carries a DIFFERENT namespace prefix than the row's own ID, but the same
// raw (post-":") suffix - e.g. a row registered under the dispatch_tasks
// call's own id ("call-1:task-a") receiving a heartbeat namespaced under a
// different coordinator context ("run-9:task-a", per dispatchNamespace's
// TaskIdentity fallback). matchesAgentID only strips a namespace off ONE
// side and compares to the OTHER side's FULL string, so two DIFFERENTLY
// namespaced ids sharing a raw suffix never match today - this must update
// the existing row, not spawn an orphan.
func TestObserveAgent_DoubleNamespacedSameSuffixUpdatesExistingRow(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "call-1",
			Name:       "dispatch_tasks",
			Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "task-a", "prompt": "a"},
				},
			},
		},
	}})
	got := next.(Screen)

	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "run-9:task-a",
			Progress:   &uievent.Progress{Status: "running", Step: 5, ToolCalls: 7},
		},
	}})
	got = next.(Screen)

	if len(got.panel.agents) != 1 {
		t.Fatalf("agents = %d rows, want 1 (heartbeat must update the existing row, not create an orphan); rows: %+v", len(got.panel.agents), got.panel.agents)
	}
	row := got.panel.agents[0]
	if row.ID != "call-1:task-a" {
		t.Errorf("row ID = %q, want unchanged %q", row.ID, "call-1:task-a")
	}
	if row.Step != 5 {
		t.Errorf("Step = %d, want 5 (live update from the differently-namespaced heartbeat)", row.Step)
	}
	if row.ToolCalls != 7 {
		t.Errorf("ToolCalls = %d, want 7", row.ToolCalls)
	}
}
