// cancel_subagent_task_key_test.go proves the "x" ContextFiles keybinding
// (keymap.IDCancelSubagentTask) forwards to the selected subagent row's
// CancelSubagentTask, following cancel_tool_call_key_test.go's local
// -keypress double style.
package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// recordingSubagentThreads is the ports.SubagentThreads test double for
// this file: it records every CancelSubagentTask call and answers with the
// scripted (ok, err) pair.
type recordingSubagentThreads struct {
	stubThreads
	canceled []string
	ok       bool
	err      error
}

func (r *recordingSubagentThreads) CancelSubagentTask(callID string) (bool, error) {
	r.canceled = append(r.canceled, callID)
	return r.ok, r.err
}

// selectSubagentRow focuses the files panel with one subagent row and
// moves the list cursor onto it (no file diffs, so the subagent is row 0).
func selectSubagentRow(t *testing.T, id string) Screen {
	t.Helper()
	s := openPanel(t, panelScreen(t, 100, 24))
	s.panel.observeAgentStart(id, id)
	s.panel.list.MoveTo(0)
	if _, isAgent := s.panel.selectedAgent(); !isAgent {
		t.Fatal("setup: the subagent row is not selected")
	}
	return s
}

// TestCancelSubagentTaskKey_ForwardsToSubagentThreads proves pressing "x"
// while a subagent row holds the panel's selection calls
// CancelSubagentTask with that row's ID.
func TestCancelSubagentTaskKey_ForwardsToSubagentThreads(t *testing.T) {
	threads := &recordingSubagentThreads{stubThreads: stubThreads{}, ok: true}
	s := selectSubagentRow(t, "agent-0")
	s.threads = threads

	next, cmd := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	// The cancel runs in the returned Cmd, off the update goroutine
	// (cancel_subagent_task.go), so nothing has been called yet.
	if len(threads.canceled) != 0 {
		t.Fatalf("CancelSubagentTask ran inline on the update goroutine: %v", threads.canceled)
	}
	if cmd == nil {
		t.Fatal("handleKey returned no Cmd; the cancel would never run")
	}
	if _, ok := cmd().(subagentTaskCancelResultMsg); !ok {
		t.Fatal("the Cmd did not report a subagentTaskCancelResultMsg")
	}
	if len(threads.canceled) != 1 || threads.canceled[0] != "agent-0" {
		t.Fatalf("CancelSubagentTask calls = %v, want exactly [\"agent-0\"]", threads.canceled)
	}
}

// TestCancelSubagentTaskKey_NoSelectionIsNoOp proves the key does nothing
// when the panel's selection is a file row, not a subagent.
func TestCancelSubagentTaskKey_NoSelectionIsNoOp(t *testing.T) {
	threads := &recordingSubagentThreads{stubThreads: stubThreads{}, ok: true}
	s := openPanel(t, panelScreen(t, 100, 24, sampleDiffs()...))
	s.threads = threads
	s.panel.list.MoveTo(0) // a file row, no subagent tracked

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.canceled) != 0 {
		t.Fatalf("CancelSubagentTask was called with a file row selected: %v", threads.canceled)
	}
}

// TestCancelSubagentTaskKey_NilThreadsIsNoOp proves the key does not panic
// when no SubagentThreads seam is wired.
func TestCancelSubagentTaskKey_NilThreadsIsNoOp(t *testing.T) {
	s := selectSubagentRow(t, "agent-0")
	s.threads = nil

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'}) // must not panic
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
}
