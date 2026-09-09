package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestTurnStreamRefusesSendsAfterClose pins the property the whole type
// exists for: once the stream is closed, no send may reach the channel.
//
// Both Send and TrySend re-check `s.closed` under the read lock before
// touching s.ch. That check is what makes the close safe - a sender that
// skipped it would panic with "send on closed channel" on the agent-loop
// goroutine. Exercising it directly also proves Close is idempotent and
// reports the single winner.
func TestTurnStreamRefusesSendsAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan uievent.Event, turnBufferSize)
	s := newTurnStream(ch, ctx.Done(), cancel)

	ev := uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "x"}}
	if !s.Send(ev) {
		t.Fatal("Send on an open stream was refused")
	}
	if !s.TrySend(ev) {
		t.Fatal("TrySend on an open stream was refused")
	}
	if s.Closed() {
		t.Fatal("stream reports closed before Close")
	}

	if !s.Close() {
		t.Fatal("Close did not report itself the winner")
	}
	if !s.Closed() {
		t.Fatal("stream does not report closed after Close")
	}
	// Exactly one close: a second caller must not close again (that would
	// panic) and must not claim the win.
	if s.Close() {
		t.Error("a second Close claimed to be the winner; the channel would be closed twice")
	}

	// The two guards under audit. Reaching the channel here would panic.
	if s.Send(ev) {
		t.Error("Send accepted an event after close")
	}
	if s.TrySend(ev) {
		t.Error("TrySend accepted an event after close")
	}
}

// TestTurnStreamTrySendDropsOnAFullBuffer pins the terminal-event trade:
// a full buffer means the reader stopped draining, and the turn goroutine
// must not park there forever.
func TestTurnStreamTrySendDropsOnAFullBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan uievent.Event, 1)
	s := newTurnStream(ch, ctx.Done(), cancel)

	ev := uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "x"}}
	if !s.TrySend(ev) {
		t.Fatal("TrySend refused the first event into an empty buffer")
	}
	if s.TrySend(ev) {
		t.Error("TrySend blocked-or-accepted on a full buffer; it must drop instead")
	}
}
