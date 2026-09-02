// cancel_tool_call_direct_test.go proves cancelFocusedToolCall's own guard
// clause directly, one branch at a time, rather than through handleKey's
// "x" keybinding dispatch (cancel_tool_call_key_test.go). The guard is a
// single line ORing four conditions together
// (!ok || Kind != KindToolStart || State != "running" || CallID == ""),
// and a mutation of any one `||` to `&&` needs a test that isolates that
// exact condition - a test that only ever exercises the keybinding path
// can leave some of the four independently unproven.
package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// pushFocusedTurnStartBlock pushes a non-tool-call block (KindTurnStart)
// and focuses it, for the "focused block is not a tool call" scenario.
func pushFocusedTurnStartBlock(t *testing.T, s Screen) Screen {
	t.Helper()
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "hello"},
	})
	s.transcript = s.transcript.FocusPrev()
	if !s.transcript.Focused() {
		t.Fatal("setup: transcript did not take focus after FocusPrev")
	}
	return s
}

// TestCancelFocusedToolCall_NothingFocusedIsNoOp proves the direct call is
// a no-op when the composer holds focus (transcript.FocusedBlock's ok ==
// false), even though s.active would record a call if reached. Isolates
// the "!ok" leg of the OR.
func TestCancelFocusedToolCall_NothingFocusedIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-1"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	// Nothing focused: s.transcript.Focused() == false (composer holds it).

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called with nothing focused: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelFocusedToolCall_FocusedBlockNotToolCallIsNoOp proves the
// direct call is a no-op when the focused block is not a tool-call block
// at all (KindTurnStart, not KindToolStart). Isolates the "Kind !=
// KindToolStart" leg of the OR.
func TestCancelFocusedToolCall_FocusedBlockNotToolCallIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-2"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushFocusedTurnStartBlock(t, s)

	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart {
		t.Fatalf("setup: expected a focused non-tool-call block, got kind=%q ok=%v", block.Kind, ok)
	}

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called on a non-tool-call block: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelFocusedToolCall_NotRunningIsNoOp proves the direct call is a
// no-op when the focused block is a tool call that already reached its
// terminal state. Isolates the `State != "running"` leg of the OR at the
// cancelFocusedToolCall call site itself (rather than only through
// handleKey, which TestCancelToolCallKey_FocusedBlockNotRunningIsNoOp
// already covers).
func TestCancelFocusedToolCall_NotRunningIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-3"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushRunningToolBlock(t, s, "call-done")
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "call-done"},
	})
	if !s.transcript.Focused() {
		t.Fatal("setup: focus was lost when the call reached its terminal state")
	}
	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Header.State == "running" {
		t.Fatalf("setup: expected a focused, non-running tool-call block, got state=%q ok=%v", block.Header.State, ok)
	}

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called on a call that already finished: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelFocusedToolCall_EmptyCallIDIsNoOp proves the direct call is a
// no-op when the focused block is a running tool call but its CallID is
// empty. Isolates the `CallID == ""` leg of the OR.
func TestCancelFocusedToolCall_EmptyCallIDIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-4"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushRunningToolBlock(t, s, "")

	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Kind != uievent.KindToolStart || block.Header.State != "running" || block.CallID != "" {
		t.Fatalf("setup: expected a focused running tool-call block with empty CallID, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called with an empty CallID: %v", handle.cancelToolCallIDs)
	}
}

// pushFocusedImpossibleBlock pushes and focuses a Block whose Kind is
// NOT KindToolStart but whose Header.State IS "running" - a combination
// no production event handler ever produces (handleToolStart is the only
// writer of State == "running", and it always pairs that with Kind ==
// KindToolStart). It exists to isolate the two `||` legs of
// cancelFocusedToolCall's guard that a realistic Block can never separate
// (Kind != KindToolStart, and State != "running"): with real block
// construction, State == "running" implies Kind == KindToolStart, so any
// test built from ordinary events cannot make one leg false while the
// other is also false. transcript.PushBlockForTest (internal/ui/component/
// transcript/export_test.go) exists solely to construct this otherwise
// unreachable field combination for that purpose.
func pushFocusedImpossibleBlock(t *testing.T, s Screen, callID string) Screen {
	t.Helper()
	s.transcript = transcript.PushBlockForTest(s.transcript, transcript.Block{
		Kind:   uievent.KindTurnStart,
		CallID: callID,
		Header: transcript.Header{Label: "not-a-tool-call", State: "running"},
	})
	s.transcript = s.transcript.FocusPrev()
	if !s.transcript.Focused() {
		t.Fatal("setup: transcript did not take focus after FocusPrev")
	}
	return s
}

// TestCancelFocusedToolCall_NonToolBlockWithRunningStateIsNoOp proves the
// guard treats "not a tool call" as disqualifying on its own, even for a
// (production-unreachable) block that also happens to carry State ==
// "running" and a non-empty CallID. Without this test, mutating the FIRST
// `||` in the guard to `&&` survives: `!ok && Kind != KindToolStart` can
// never be observed to differ from `!ok` alone when !ok is true (a
// composer-focused, zero-value Block always has Kind == "", which is
// already != KindToolStart) - so proving that leg's effect at all requires
// a focused block where !ok is FALSE (something real is focused) and Kind
// != KindToolStart is the ONLY clause that should still make the whole
// guard true. State == "running" and CallID != "" must both be forced
// true (not just left at their zero values) so those two legs cannot
// accidentally cover for a broken first leg.
func TestCancelFocusedToolCall_NonToolBlockWithRunningStateIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-5"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushFocusedImpossibleBlock(t, s, "call-impossible")

	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart || block.Header.State != "running" || block.CallID == "" {
		t.Fatalf("setup: expected a focused non-tool-call block with State=running and a CallID, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called on a non-tool-call block: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelFocusedToolCall_NonToolBlockNotRunningIsStillNoOp is the
// companion to the test above, isolating the SECOND `||` in the guard
// (between `Kind != KindToolStart` and `State != "running"`). Mutating it
// to `&&` produces `!ok || (Kind != KindToolStart && State != "running") ||
// CallID == ""`, which - like the first leg - is unobservable from any
// block a real event handler can build, because State == "running"
// implies Kind == KindToolStart there too. This test forces Kind !=
// KindToolStart true while ALSO forcing State != "running" true (an
// ordinary, not-running non-tool block), which the mutant would still
// treat as a no-op because the OR still finds "Kind != KindToolStart"
// true on its own; paired with the test above (which forces the same Kind
// leg true while State != "running" is FALSE), the two together fully
// separate what each of the first two `||` legs contributes.
func TestCancelFocusedToolCall_NonToolBlockNotRunningIsStillNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-direct-6"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushFocusedTurnStartBlock(t, s)

	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Kind == uievent.KindToolStart || block.Header.State == "running" {
		t.Fatalf("setup: expected a focused, not-running non-tool-call block, got %+v ok=%v", block, ok)
	}

	next, cmd := s.cancelFocusedToolCall()
	if cmd != nil {
		t.Fatalf("cancelFocusedToolCall returned a non-nil Cmd for a no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("cancelFocusedToolCall returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called on a non-tool-call block: %v", handle.cancelToolCallIDs)
	}
}
