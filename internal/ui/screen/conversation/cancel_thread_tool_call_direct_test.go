// cancel_thread_tool_call_direct_test.go proves
// cancelFocusedThreadToolCall's two guard clauses (cancel_thread_tool_call.go)
// directly, one branch at a time, rather than through threadDialogScrollKey's
// "x" keybinding dispatch (cancel_thread_tool_call_key_test.go). It mirrors
// cancel_tool_call_direct_test.go's approach for the main transcript's
// cancelFocusedToolCall exactly, including transcript.PushBlockForTest for
// otherwise-unreachable field combinations, scoped to the embedded thread
// dialog's own state (s.thread, s.threads, s.threadID, s.thread.transcript)
// instead of the outer screen's.
package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// openValidThreadDialog opens the "sa-1" thread dialog on a fresh screen
// with the SubagentThreads seam wired, returning it with s.thread,
// s.threads, and s.threadID all set to valid, non-empty values - the
// baseline every existence-guard test below mutates exactly one field of.
func openValidThreadDialog(t *testing.T, threads *recordingThreadCancelThreads) Screen {
	t.Helper()
	s := threadScreen(t, threads, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil || s.threads == nil || s.threadID == "" {
		t.Fatalf("setup: thread dialog did not open with thread/threads/threadID all set: thread=%v threads=%v threadID=%q", s.thread, s.threads, s.threadID)
	}
	return s
}

// TestCancelFocusedThreadToolCall_NilThreadOnlyIsNoop isolates the
// `s.thread == nil` leg of the existence guard: s.threads and s.threadID
// stay valid, only s.thread is cleared.
func TestCancelFocusedThreadToolCall_NilThreadOnlyIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s.thread = nil

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called with s.thread == nil: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_NilThreadsOnlyIsNoop isolates the
// `s.threads == nil` leg: s.thread and s.threadID stay valid, only
// s.threads is cleared. A broken guard here would reach
// s.threads.CancelSubagentToolCall on a nil interface and panic.
func TestCancelFocusedThreadToolCall_NilThreadsOnlyIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadRunningToolBlock(t, s, "tc-nilthreads")
	s.threads = nil

	next, cmd := s.cancelFocusedThreadToolCall() // must not panic
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
}

// TestCancelFocusedThreadToolCall_EmptyThreadIDOnlyIsNoop isolates the
// `s.threadID == ""` leg: s.thread and s.threads stay valid, only
// s.threadID is cleared.
func TestCancelFocusedThreadToolCall_EmptyThreadIDOnlyIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadRunningToolBlock(t, s, "tc-emptyid")
	s.threadID = ""

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called with s.threadID == \"\": %v", threads.callIDs)
	}
}

// pushThreadRunningToolBlock pushes a running tool-call block into the
// embedded thread dialog's OWN transcript (s.thread.transcript, not the
// main s.transcript) and focuses it - the direct-call analogue of
// openThreadDialogWithRunningTool (cancel_thread_tool_call_key_test.go),
// factored out so callers can push additional blocks onto an
// already-opened dialog without reopening it.
func pushThreadRunningToolBlock(t *testing.T, s Screen, callID string) Screen {
	t.Helper()
	s.thread.transcript, _ = s.thread.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: callID, Name: "run_command"},
	})
	s.thread.transcript = s.thread.transcript.FocusPrev()
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: thread transcript did not take focus after FocusPrev")
	}
	return s
}

// pushThreadFocusedTurnStartBlock pushes a non-tool-call block
// (KindTurnStart) into the thread dialog's own transcript and focuses it,
// for the "focused block is not a tool call" scenario.
func pushThreadFocusedTurnStartBlock(t *testing.T, s Screen) Screen {
	t.Helper()
	s.thread.transcript, _ = s.thread.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "hello"},
	})
	s.thread.transcript = s.thread.transcript.FocusPrev()
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: thread transcript did not take focus after FocusPrev")
	}
	return s
}

// pushThreadFocusedImpossibleBlock pushes and focuses a Block whose Kind
// is NOT KindToolStart but whose Header.State IS "running" - a combination
// no production event handler ever produces in the thread transcript
// either. See cancel_tool_call_direct_test.go's pushFocusedImpossibleBlock
// for the full rationale; this is its thread-scoped twin.
func pushThreadFocusedImpossibleBlock(t *testing.T, s Screen, callID string) Screen {
	t.Helper()
	s.thread.transcript = transcript.PushBlockForTest(s.thread.transcript, transcript.Block{
		Kind:   uievent.KindTurnStart,
		CallID: callID,
		Header: transcript.Header{Label: "not-a-tool-call", State: "running"},
	})
	s.thread.transcript = s.thread.transcript.FocusPrev()
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: thread transcript did not take focus after FocusPrev")
	}
	return s
}

func openedThreadWithoutFocus(t *testing.T, threads *recordingThreadCancelThreads) Screen {
	t.Helper()
	s := openValidThreadDialog(t, threads)
	if s.thread.transcript.Focused() {
		t.Fatal("setup: the thread transcript must start unfocused")
	}
	return s
}

// TestCancelFocusedThreadToolCall_NothingFocusedIsNoop isolates the `!ok`
// leg of the event-state guard: nothing focused in the thread dialog's
// transcript.
func TestCancelFocusedThreadToolCall_NothingFocusedIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openedThreadWithoutFocus(t, threads)

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called with nothing focused: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_FocusedBlockNotToolCallIsNoop isolates
// the `block.Kind != uievent.KindToolStart` leg: an ordinary, focused,
// non-tool-call block.
func TestCancelFocusedThreadToolCall_FocusedBlockNotToolCallIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadFocusedTurnStartBlock(t, s)

	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart {
		t.Fatalf("setup: expected a focused non-tool-call block, got kind=%q ok=%v", block.Kind, ok)
	}

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called on a non-tool-call block: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_NotRunningIsNoop isolates the
// `block.Header.State != "running"` leg: a tool-call block that already
// reached its terminal state.
func TestCancelFocusedThreadToolCall_NotRunningIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadRunningToolBlock(t, s, "tc-done")
	s.thread.transcript, _ = s.thread.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "tc-done"},
	})
	if !s.thread.transcript.Focused() {
		t.Fatal("setup: focus was lost when the call reached its terminal state")
	}
	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Header.State == "running" {
		t.Fatalf("setup: expected a focused, non-running tool-call block, got state=%q ok=%v", block.Header.State, ok)
	}

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called on a call that already finished: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_EmptyCallIDIsNoop isolates the
// `block.CallID == ""` leg: a focused, running tool-call block with an
// empty CallID.
func TestCancelFocusedThreadToolCall_EmptyCallIDIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadRunningToolBlock(t, s, "")

	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Kind != uievent.KindToolStart || block.Header.State != "running" || block.CallID != "" {
		t.Fatalf("setup: expected a focused running tool-call block with empty CallID, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called with an empty CallID: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_NonToolBlockWithRunningStateIsNoop
// proves the guard treats "not a tool call" as disqualifying on its own,
// even for a (production-unreachable) block that also happens to carry
// State == "running" and a non-empty CallID. See
// cancel_tool_call_direct_test.go's
// TestCancelFocusedToolCall_NonToolBlockWithRunningStateIsNoOp for the
// full rationale (isolating the first `||` from a mutation to `&&`).
func TestCancelFocusedThreadToolCall_NonToolBlockWithRunningStateIsNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadFocusedImpossibleBlock(t, s, "call-impossible")

	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart || block.Header.State != "running" || block.CallID == "" {
		t.Fatalf("setup: expected a focused non-tool-call block with State=running and a CallID, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called on a non-tool-call block: %v", threads.callIDs)
	}
}

// TestCancelFocusedThreadToolCall_NonToolBlockNotRunningIsStillNoop is the
// companion to the test above, isolating the SECOND `||` in the guard
// (between `Kind != KindToolStart` and `State != "running"`). See
// cancel_tool_call_direct_test.go's
// TestCancelFocusedToolCall_NonToolBlockNotRunningIsStillNoOp for the full
// rationale.
func TestCancelFocusedThreadToolCall_NonToolBlockNotRunningIsStillNoop(t *testing.T) {
	threads := &recordingThreadCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		ok:          true,
	}
	s := openValidThreadDialog(t, threads)
	s = pushThreadFocusedTurnStartBlock(t, s)

	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart || block.Header.State == "running" {
		t.Fatalf("setup: expected a focused, not-running non-tool-call block, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedThreadToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedThreadToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(threads.callIDs) != 0 {
		t.Fatalf("CancelSubagentToolCall was called on a non-tool-call block: %v", threads.callIDs)
	}
}
