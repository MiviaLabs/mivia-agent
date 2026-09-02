// cancel_thread_tool_call_key_test.go proves the "x" key inside an open
// subagent-thread dialog (threadDialogScrollKey, thread.go) forwards to
// ports.SubagentThreads.CancelSubagentToolCall with the dialog's own
// callID and the focused block's tool-call ID, following
// cancel_tool_call_key_test.go's local-keypress recording style.
package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// recordingThreadCancelThreads is the ports.SubagentThreads test double for
// this file: it serves stubThreads' Thread() lookup and records every
// CancelSubagentToolCall call.
type recordingThreadCancelThreads struct {
	stubThreads
	callIDs, toolCallIDs []string
	ok                   bool
	err                  error
}

func (r *recordingThreadCancelThreads) CancelSubagentToolCall(callID, toolCallID string) (bool, error) {
	r.callIDs = append(r.callIDs, callID)
	r.toolCallIDs = append(r.toolCallIDs, toolCallID)
	return r.ok, r.err
}

// openThreadDialogWithRunningTool opens the "sa-1" thread dialog and pushes
// a still-running tool-call block into the DIALOG'S OWN transcript
// (s.thread.transcript, not the main s.transcript), focusing it - the
// state cancelFocusedThreadToolCall requires before it will act. Mirrors
// pushRunningToolBlock (cancel_tool_call_key_test.go), scoped to the
// embedded thread screen.
func openThreadDialogWithRunningTool(t *testing.T, threads ports.SubagentThreads, toolCallID string) Screen {
	t.Helper()
	s := threadScreen(t, threads, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("setup: thread dialog did not open")
	}
	s.thread.transcript, _ = s.thread.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: toolCallID, Name: "run_command"},
	})
	s.thread.transcript = s.thread.transcript.FocusPrev()
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: thread transcript did not take focus after FocusPrev")
	}
	return s
}

// TestCancelThreadToolCallKey_ForwardsToSubagentThreads proves pressing "x"
// while a still-running tool-call block holds the OPEN THREAD DIALOG's own
// focus calls CancelSubagentToolCall with the dialog's callID (the row
// that opened it, "sa-1") and the focused block's tool-call ID - and does
// NOT touch s.active (the ROOT turn's handle), which this dialog has
// nothing to do with.
func TestCancelThreadToolCallKey_ForwardsToSubagentThreads(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openThreadDialogWithRunningTool(t, threads, "tc-1")

	rootHandle := &recordingHandle{id: "turn-1"}
	s.active = rootHandle

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 1 || threads.callIDs[0] != "sa-1" {
		t.Fatalf("CancelSubagentToolCall callID calls = %v, want exactly [\"sa-1\"]", threads.callIDs)
	}
	if len(threads.toolCallIDs) != 1 || threads.toolCallIDs[0] != "tc-1" {
		t.Fatalf("CancelSubagentToolCall toolCallID calls = %v, want exactly [\"tc-1\"]", threads.toolCallIDs)
	}
	if len(rootHandle.cancelToolCallIDs) != 0 {
		t.Fatalf("the ROOT turn's CancelToolCall was called: %v; the thread dialog must never route through s.active", rootHandle.cancelToolCallIDs)
	}
}

// TestCancelThreadToolCallKey_NoFocusIsNoOp proves the key does nothing
// inside an open thread dialog when nothing in its transcript is focused.
func TestCancelThreadToolCallKey_NoFocusIsNoOp(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := threadScreen(t, threads, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("setup: thread dialog did not open")
	}
	if s.thread.transcript.Focused() {
		t.Fatal("setup: the thread transcript must start unfocused")
	}

	next, _ = s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called with nothing focused: %v", threads.callIDs)
	}
}

// TestCancelThreadToolCallKey_FocusedBlockNotRunningIsNoOp proves the key
// is a no-op when the focused block in the thread dialog is not a
// still-running tool call (already reached its terminal KindToolEnd).
func TestCancelThreadToolCallKey_FocusedBlockNotRunningIsNoOp(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openThreadDialogWithRunningTool(t, threads, "tc-2")
	s.thread.transcript, _ = s.thread.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "tc-2"},
	})
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: focus was lost when the call reached its terminal state")
	}

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called on a call that already finished: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_NilStateIsNoop proves
// cancelFocusedThreadToolCall does not panic on a zero-value Screen (no
// thread, no threads registry, no threadID) - the direct-call analogue of
// cancel_tool_call_direct_test.go's nil-state coverage for the main
// transcript's cancelFocusedToolCall.
func TestCancelFocusedThreadToolCall_NilStateIsNoop(t *testing.T) {
	var s Screen
	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
}
