// cancel_tool_call_key_test.go proves the "x" ContextTranscript keybinding
// (keymap.IDCancelToolCall) forwards to the active turn's CancelToolCall,
// following the local-keypress double of remote_input_cancel_test.go's
// cancelTrackingTurnHandle/recordingHandle style rather than that file's
// remote-input path.
package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// pushRunningToolBlock focuses the transcript on a freshly pushed, still
// -running tool-call block (KindToolStart, no matching KindToolEnd), the
// state cancelFocusedToolCall requires before it will act.
func pushRunningToolBlock(t *testing.T, s Screen, callID string) Screen {
	t.Helper()
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: callID, Name: "run_command"},
	})
	s.transcript = s.transcript.FocusPrev() // composer -> the block just pushed
	if !s.transcript.Focused() {
		t.Fatal("setup: transcript did not take focus after FocusPrev")
	}
	return s
}

// TestCancelToolCallKey_ForwardsToActiveTurn proves pressing "x" while a
// still-running tool-call block holds the transcript's focus calls
// CancelToolCall on the active turn handle with that block's CallID.
func TestCancelToolCallKey_ForwardsToActiveTurn(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-tool"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1", cancelToolCallResult: true}
	s.active = handle
	s = pushRunningToolBlock(t, s, "call-1")

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	_ = scr

	if len(handle.cancelToolCallIDs) != 1 || handle.cancelToolCallIDs[0] != "call-1" {
		t.Fatalf("CancelToolCall calls = %v, want exactly [\"call-1\"]", handle.cancelToolCallIDs)
	}
}

// TestCancelToolCallKey_NoFocusIsNoOp proves the key does nothing when the
// composer holds focus (nothing to act on).
func TestCancelToolCallKey_NoFocusIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-tool-2"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1"}
	s.active = handle
	// The composer holds focus; s.transcript.Focused() == false, so the
	// key never even reaches ContextTranscript's Match (see handleKey's
	// `if s.transcript.Focused()` guard) - this proves the WHOLE path is
	// a no-op, not just cancelFocusedToolCall in isolation.
	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called with nothing focused: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelToolCallKey_FocusedBlockNotRunningIsNoOp proves the key is a
// no-op when the focused block is not a still-running tool call (here: a
// tool call that has already reached its terminal KindToolEnd state).
func TestCancelToolCallKey_FocusedBlockNotRunningIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-tool-3"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &recordingHandle{id: "turn-1"}
	s.active = handle
	s = pushRunningToolBlock(t, s, "call-2")
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "call-2"},
	})
	if !s.transcript.Focused() {
		t.Fatal("setup: focus was lost when the call reached its terminal state")
	}

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
	if len(handle.cancelToolCallIDs) != 0 {
		t.Fatalf("CancelToolCall was called on a call that already finished: %v", handle.cancelToolCallIDs)
	}
}

// TestCancelToolCallKey_NoActiveTurnIsNoOp proves the key does not panic
// when a running tool-call block is focused but no turn is active (a stale
// or already-finished transcript view).
func TestCancelToolCallKey_NoActiveTurnIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-tool-4"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	// s.active is nil (New never sets it).
	s = pushRunningToolBlock(t, s, "call-3")

	next, _ := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'}) // must not panic
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleKey returned a non-Screen app.Screen: %T", next)
	}
}
