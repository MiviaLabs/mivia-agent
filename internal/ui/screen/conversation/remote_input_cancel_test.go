package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// cancelTrackingTurnHandle is a ports.TurnHandle test double that records
// whether Cancel() was called, so a test can assert a remote "cancel" input
// actually reached the active turn - the same call cancelTurn() (keys.go)
// makes for local Ctrl+C/Esc.
type cancelTrackingTurnHandle struct {
	id        string
	events    chan uievent.Event
	cancelled bool
}

func (h *cancelTrackingTurnHandle) ID() string                   { return h.id }
func (h *cancelTrackingTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *cancelTrackingTurnHandle) Cancel()                      { h.cancelled = true }
func (h *cancelTrackingTurnHandle) CancelToolCall(string) bool   { return false }

// TestHandleRemoteInput_CancelKindCancelsActiveTurn proves a "cancel" remote
// input event, targeted at the foreground session with an active turn,
// calls Cancel() on it - mirroring cancelTurn()'s local Ctrl+C/Esc behavior.
func TestHandleRemoteInput_CancelKindCancelsActiveTurn(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	handle := &cancelTrackingTurnHandle{id: "turn-1", events: make(chan uievent.Event)}
	s.active = handle

	next, cmd := s.handleRemoteInput(ports.RemoteInputEvent{
		ID: "input-1", SessionID: "sess-cancel", Kind: "cancel",
	})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteInput returned a non-Screen app.Screen: %T", next)
	}

	if !handle.cancelled {
		t.Error("remote cancel input did not call Cancel() on the active turn handle")
	}
	// The rearm Cmd for the next remote input is still returned even though
	// this screen was never wired with SetRemoteInputs - awaitRemoteInput
	// returns nil in that case, so cmd may be nil; either is fine as long as
	// it does not panic.
	_ = cmd
	_ = scr
}

// TestHandleRemoteInput_CancelKindNoActiveTurnIsNoOp proves a "cancel"
// remote input arriving after the turn already finished (s.active == nil)
// is a silent no-op: no panic, no error, no crash.
func TestHandleRemoteInput_CancelKindNoActiveTurnIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-cancel-2"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	// s.active is nil (New never sets it).

	next, _ := s.handleRemoteInput(ports.RemoteInputEvent{
		ID: "input-2", SessionID: "sess-cancel-2", Kind: "cancel",
	})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteInput returned a non-Screen app.Screen: %T", next)
	}
	if scr.active != nil {
		t.Error("no-op remote cancel unexpectedly set an active turn")
	}
}

// TestHandleRemoteInput_CancelKindTargetsBackgroundSession proves the
// background-session path (s.sessions) also cancels its own active turn
// without touching the foreground one.
func TestHandleRemoteInput_CancelKindTargetsBackgroundSession(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-fg"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	fgHandle := &cancelTrackingTurnHandle{id: "turn-fg", events: make(chan uievent.Event)}
	s.active = fgHandle

	bgHandle := &cancelTrackingTurnHandle{id: "turn-bg", events: make(chan uievent.Event)}
	s.sessions["sess-bg"] = &sessionState{active: bgHandle}

	next, _ := s.handleRemoteInput(ports.RemoteInputEvent{
		ID: "input-3", SessionID: "sess-bg", Kind: "cancel",
	})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteInput returned a non-Screen app.Screen: %T", next)
	}

	if !bgHandle.cancelled {
		t.Error("remote cancel targeted at background session did not cancel its turn")
	}
	if fgHandle.cancelled {
		t.Error("remote cancel targeted at background session incorrectly cancelled the foreground turn")
	}
	if scr.active != fgHandle {
		t.Error("foreground active handle was unexpectedly replaced")
	}
}

// TestHandleRemoteInput_CancelKindUntrackedSessionIsNoOp proves a "cancel"
// remote input targeted at a session this screen has never tracked (not the
// foreground session, absent from s.sessions) is a silent no-op: no panic,
// and the foreground turn is left untouched. This distinguishes the
// "session not tracked" (!ok) refusal from the "tracked but idle"
// (st.active == nil) refusal - handleRemoteCancel's `!ok || st.active ==
// nil` guard short-circuits on !ok before ever touching st.active, which
// would nil-pointer-deref on a genuinely untracked key.
func TestHandleRemoteInput_CancelKindUntrackedSessionIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-fg-2"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	fgHandle := &cancelTrackingTurnHandle{id: "turn-fg-2", events: make(chan uievent.Event)}
	s.active = fgHandle

	next, _ := s.handleRemoteInput(ports.RemoteInputEvent{
		ID: "input-4", SessionID: "sess-never-tracked", Kind: "cancel",
	})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteInput returned a non-Screen app.Screen: %T", next)
	}
	if fgHandle.cancelled {
		t.Error("remote cancel targeted at an untracked session incorrectly cancelled the foreground turn")
	}
	if scr.active != fgHandle {
		t.Error("foreground active handle was unexpectedly replaced")
	}
}

// TestHandleRemoteInput_CancelKindTrackedSessionNoActiveTurnIsNoOp proves a
// "cancel" remote input targeted at a background session this screen DOES
// track, but which has no active turn (st.active == nil), is a silent
// no-op - the other operand of handleRemoteCancel's `!ok || st.active ==
// nil` guard.
func TestHandleRemoteInput_CancelKindTrackedSessionNoActiveTurnIsNoOp(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-fg-3"}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)

	fgHandle := &cancelTrackingTurnHandle{id: "turn-fg-3", events: make(chan uievent.Event)}
	s.active = fgHandle
	s.sessions["sess-bg-idle"] = &sessionState{active: nil}

	next, _ := s.handleRemoteInput(ports.RemoteInputEvent{
		ID: "input-5", SessionID: "sess-bg-idle", Kind: "cancel",
	})
	scr, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteInput returned a non-Screen app.Screen: %T", next)
	}
	if fgHandle.cancelled {
		t.Error("remote cancel targeted at an idle background session incorrectly cancelled the foreground turn")
	}
	if scr.active != fgHandle {
		t.Error("foreground active handle was unexpectedly replaced")
	}
}
