package hub

import (
	"errors"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestTurnTerminalsAreRelayed replaces TestTurnTerminalsAreNotRelayed, which
// pinned the opposite. The omission it guarded was real while any of the three
// preconditions in relayedKinds' comment was unmet; all three now hold, and a
// second surface that never learns a turn ended cannot tell a finished turn
// from a stalled one.
func TestTurnTerminalsAreRelayed(t *testing.T) {
	for _, kind := range []events.Kind{events.KindTurnEnd, events.KindError} {
		if !slices.Contains(relayedKinds, kind) {
			t.Errorf("%q is not relayed; a second surface cannot tell a finished turn from a stalled one", kind)
		}
	}
}

// TestTurnStartIsStillRelayed guards the other half: the terminals must not be
// added by widening the list into something that drops turn starts, which carry
// the user's own submitted text.
func TestTurnStartIsStillRelayed(t *testing.T) {
	if !slices.Contains(relayedKinds, events.KindTurnStart) {
		t.Error("KindTurnStart is no longer relayed; a second surface loses the user's own text")
	}
}

// TestRelayDeliversATerminalAfterTheTurnItCloses is the ordering claim made
// concrete for the kinds that were withheld because of it. A terminal that
// overtakes its own turn's content is exactly the failure the omission existed
// to avoid, so relaying terminals without this assertion would be a promise
// with no gate behind it.
func TestRelayDeliversATerminalAfterTheTurnItCloses(t *testing.T) {
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()
	c := attachIdleClient(t, o, 1)

	turn := []events.Event{
		{Kind: events.KindTurnStart, SessionID: "sess-owner", TurnID: "turn:1", Detail: "hi"},
		{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:1", Content: "a", Detail: "delta"},
		{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:1", Content: "b", Detail: "delta"},
		{Kind: events.KindTurnEnd, SessionID: "sess-owner", TurnID: "turn:1"},
	}
	for _, ev := range turn {
		sess.EventBus.Publish(ev)
	}
	sess.EventBus.Flush()

	got := make([]string, 0, len(turn))
	for len(c.out) > 0 {
		got = append(got, (<-c.out).Kind)
	}
	want := []string{
		string(events.KindTurnStart), string(events.KindAssistant),
		string(events.KindAssistant), string(events.KindTurnEnd),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("relayed kinds = %v, want %v (the terminal must not overtake its own turn)", got, want)
	}
}

// TestRelayedErrorCarriesNoRawProviderText is the privacy claim made concrete
// for the newly relayed kind. KindError is the only relayed kind that can carry
// an error at all, so this is the event that made the wire's classification
// necessary rather than theoretical.
func TestRelayedErrorCarriesNoRawProviderText(t *testing.T) {
	const secret = "SECRET-PROMPT-TEXT-9f3a"
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()
	c := attachIdleClient(t, o, 1)

	sess.EventBus.Publish(events.Event{
		Kind: events.KindError, SessionID: "sess-owner", TurnID: "turn:1",
		Err: errors.New("provider rejected request: " + secret),
	})
	sess.EventBus.Flush()

	if len(c.out) != 1 {
		t.Fatalf("queued %d events for the client, want 1", len(c.out))
	}
	w := <-c.out
	if w.ErrorText == "" {
		t.Fatal("the relayed error carried no message at all; the receiver cannot report the failure")
	}
	if w.ErrorText != "chat turn failed" {
		t.Fatalf("ErrorText = %q, want the classified message", w.ErrorText)
	}
}
