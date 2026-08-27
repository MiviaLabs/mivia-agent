package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// A background turn's events must keep draining even when the session that
// started them is not (or no longer) tracked in s.sessions - e.g. an event
// delivered after the app lost track of that session's bookkeeping. Before
// the fix, handleEventMsg's map-miss branch returned a nil tea.Cmd, ending
// the read loop for that channel forever: any further event the writer
// tried to send would sit in the channel until it filled, and the writer -
// invoked synchronously from the agent loop for a live turn - would then
// block indefinitely trying to emit the NEXT event. Losing the ability to
// render an untracked session's transcript is an acceptable, much smaller
// loss than stalling that turn's execution.
func TestHandleEventMsg_UnknownSessionKeepsDrainingItsOwnChannel(t *testing.T) {
	s, _, _, _ := setupTwoSessionScreen(t)

	ch := make(chan uievent.Event, 4)
	msg := uievent.EventMsg{
		SessionID: "sess-ghost", // never registered via switchConversation
		Event: uievent.Event{
			Kind:   uievent.KindTextDelta,
			TurnID: "turn-ghost",
			Body:   uievent.TextDeltaBody{Text: "first"},
		},
		Source: ch,
	}

	_, cmd := s.handleEventMsg(msg)
	if cmd == nil {
		t.Fatal("handleEventMsg for an untracked session returned a nil Cmd: the read loop for its channel died, so a filled channel would block its writer forever")
	}

	// The returned Cmd must read from the SAME channel the event came
	// from, not some unrelated/empty one: prove it by pushing a second
	// event and confirming the Cmd yields it.
	ch <- uievent.Event{Kind: uievent.KindTextDelta, TurnID: "turn-ghost", Body: uievent.TextDeltaBody{Text: "second"}}
	got := cmd()
	em, ok := got.(uievent.EventMsg)
	if !ok {
		t.Fatalf("Cmd() returned %T, want uievent.EventMsg", got)
	}
	body, ok := em.Event.Body.(uievent.TextDeltaBody)
	if !ok || body.Text != "second" {
		t.Fatalf("Cmd() delivered %+v, want the second event pushed onto the same channel", em)
	}
}

// Sanity check: a genuinely nil Source (a caller with no channel to
// self-heal from) must not panic - it degrades to the old silent-drop
// behavior rather than crashing.
func TestHandleEventMsg_UnknownSessionNilSourceDoesNotPanic(t *testing.T) {
	s, _, _, _ := setupTwoSessionScreen(t)

	msg := uievent.EventMsg{SessionID: "sess-ghost", Event: uievent.Event{Kind: uievent.KindTextDelta}}
	_, cmd := s.handleEventMsg(msg)
	if cmd != nil {
		t.Errorf("expected nil Cmd with nil Source, got %v", cmd)
	}
	_ = tea.Msg(nil)
}
