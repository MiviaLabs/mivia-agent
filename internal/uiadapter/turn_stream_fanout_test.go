package uiadapter

// turnStream's refusal arms. A refused send is not a dropped event when a
// live viewer is attached: the automation watch surface reads through the
// fanout tee, so a terminal event must still reach it after the primary
// buffer has stopped draining. That is the contract this file pins.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestTrySendFansOutWhenThePrimaryBufferIsFull covers TrySend's default
// arm: the unbuffered channel has no reader, so the primary send is
// refused, but a registered viewer tee must still receive the event.
// Without this, a watched automation run stops updating the moment the
// foreground stops draining.
func TestTrySendFansOutWhenThePrimaryBufferIsFull(t *testing.T) {
	ch := make(chan uievent.Event) // no reader: every TrySend hits default
	done := make(chan struct{})
	stream := newTurnStream(ch, done, func() {})

	var teed []uievent.Event
	stream.fanout = func(e uievent.Event) { teed = append(teed, e) }

	accepted := stream.TrySend(uievent.Event{TurnID: "t1", Seq: 1})
	if accepted {
		t.Fatal("TrySend into an undrained unbuffered channel returned true, want false")
	}
	if len(teed) != 1 {
		t.Fatalf("fanout saw %d events, want 1 (a refused primary send must still reach a live viewer)", len(teed))
	}
	if teed[0].TurnID != "t1" {
		t.Fatalf("teed event TurnID = %q, want t1", teed[0].TurnID)
	}
}

// TestTrySendWithoutAViewerJustRefuses pins the nil-fanout arm: with no
// viewer attached the refusal is a plain false and must not panic.
func TestTrySendWithoutAViewerJustRefuses(t *testing.T) {
	ch := make(chan uievent.Event)
	stream := newTurnStream(ch, make(chan struct{}), func() {})

	if stream.TrySend(uievent.Event{Seq: 1}) {
		t.Fatal("TrySend into an undrained channel returned true, want false")
	}
}

// TestTrySendAfterCloseIsRefused pins the closed arm: once the stream is
// closed no further event may be accepted, so a late agent-loop event
// cannot resurrect a finished turn.
func TestTrySendAfterCloseIsRefused(t *testing.T) {
	ch := make(chan uievent.Event, 4)
	stream := newTurnStream(ch, make(chan struct{}), func() {})

	if !stream.TrySend(uievent.Event{Seq: 1}) {
		t.Fatal("TrySend into a buffered open stream returned false, want true")
	}
	if !stream.Close() {
		t.Fatal("first Close returned false, want true")
	}
	if stream.TrySend(uievent.Event{Seq: 2}) {
		t.Fatal("TrySend after Close returned true, want false")
	}
	if !stream.Closed() {
		t.Fatal("Closed() = false after Close()")
	}
}
