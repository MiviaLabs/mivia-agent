package replay

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func fixtureEvents() []uievent.Event {
	return []uievent.Event{
		{Kind: uievent.KindTurnStart, TurnID: "recorded", Seq: 1, Body: uievent.TurnStartBody{Input: "hi"}},
		{Kind: uievent.KindTextEnd, TurnID: "recorded", Seq: 2, Body: uievent.TextEndBody{Text: "hello"}},
		{Kind: uievent.KindTurnEnd, TurnID: "recorded", Seq: 3, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
}

func TestConversationSendReplaysInOrder(t *testing.T) {
	c := New(fixtureEvents(), 0)
	handle, err := c.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	var got []uievent.Event
	for ev := range handle.Events() {
		got = append(got, ev)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Kind != uievent.KindTurnStart || got[2].Kind != uievent.KindTurnEnd {
		t.Errorf("events out of order: %v", got)
	}
}

func TestConversationStampsHandleTurnID(t *testing.T) {
	c := New(fixtureEvents(), 0)
	handle, err := c.Send(context.Background(), intent.Send{})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range handle.Events() {
		if ev.TurnID != handle.ID() {
			t.Errorf("event TurnID %q != handle ID %q (fixture's own recorded TurnID must not leak through)", ev.TurnID, handle.ID())
		}
	}
}

func TestConversationDistinctTurnIDsAcrossSends(t *testing.T) {
	c := New(fixtureEvents(), 0)
	h1, _ := c.Send(context.Background(), intent.Send{})
	for range h1.Events() {
	}
	h2, _ := c.Send(context.Background(), intent.Send{})
	for range h2.Events() {
	}
	if h1.ID() == h2.ID() {
		t.Errorf("expected distinct turn ids, both were %q", h1.ID())
	}
}

func TestConversationCancelStopsReplay(t *testing.T) {
	c := New(fixtureEvents(), 10*time.Millisecond)
	handle, err := c.Send(context.Background(), intent.Send{})
	if err != nil {
		t.Fatal(err)
	}
	first, ok := <-handle.Events()
	if !ok || first.Kind != uievent.KindTurnStart {
		t.Fatalf("expected first event to arrive, got %+v ok=%v", first, ok)
	}
	handle.Cancel()

	select {
	case _, ok := <-handle.Events():
		if ok {
			t.Error("expected no further events after Cancel (may occasionally race the in-flight delay, but the channel must close)")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close within 1s of Cancel")
	}
}

func TestConversationCancelIsIdempotent(t *testing.T) {
	c := New(fixtureEvents(), 0)
	handle, _ := c.Send(context.Background(), intent.Send{})
	for range handle.Events() {
	}
	handle.Cancel()
	handle.Cancel() // must not panic
}

func TestConversationContextCancellation(t *testing.T) {
	c := New(fixtureEvents(), time.Hour) // long pace: only ctx cancellation stops it
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := c.Send(ctx, intent.Send{})
	if err != nil {
		t.Fatal(err)
	}
	<-handle.Events()
	cancel()
	select {
	case <-handle.Events():
	case <-time.After(time.Second):
		t.Fatal("channel did not close within 1s of context cancellation")
	}
}

func TestConversationStaticInfo(t *testing.T) {
	c := New(nil, 0)
	if c.History() != nil {
		t.Errorf("expected nil history from the replay fake, got %v", c.History())
	}
	if got := c.Model().Name; got != "replay" {
		t.Errorf("got model name %q, want \"replay\"", got)
	}
}

func TestConversationContextUsageIsZero(t *testing.T) {
	c := New(nil, 0)
	got := c.ContextUsage()
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.CachedTokens != 0 || got.CostUSD != 0 {
		t.Errorf("got %+v, want the zero value from a fixture player with no real accounting", got)
	}
}

// TestConversationCancelDuringUnreadSend forces the goroutine's cancellation
// check on the *send* itself: with nothing ever reading from Events(), the
// unbuffered channel send blocks immediately, so Cancel must be observed
// there, not in the inter-event pace wait.
func TestConversationCancelDuringUnreadSend(t *testing.T) {
	c := New(fixtureEvents(), 0)
	handle, err := c.Send(context.Background(), intent.Send{})
	if err != nil {
		t.Fatal(err)
	}
	handle.Cancel()
	// Give the goroutine a chance to observe the cancellation and close ch
	// before this test ever reads from it. Reading immediately would make
	// this select itself a ready receiver, racing the goroutine's send
	// case against its cancelCh case instead of proving cancellation wins
	// when nothing is listening.
	time.Sleep(20 * time.Millisecond)

	select {
	case _, ok := <-handle.Events():
		if ok {
			t.Error("expected the channel to close without ever delivering an event")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close within 1s of cancelling before any read")
	}
}

// TestConversationContextDoneDuringUnreadSend is the ctx.Done() analogue of
// TestConversationCancelDuringUnreadSend: the send-side select's ctx.Done()
// branch, not the pace-wait one.
func TestConversationContextDoneDuringUnreadSend(t *testing.T) {
	c := New(fixtureEvents(), 0)
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := c.Send(ctx, intent.Send{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond) // see TestConversationCancelDuringUnreadSend

	select {
	case _, ok := <-handle.Events():
		if ok {
			t.Error("expected the channel to close without ever delivering an event")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close within 1s of context cancellation before any read")
	}
}

// TestConversationPaceDelaysBetweenEvents pins the pace parameter, which is
// the only reason Send waits at all. A three-event fixture at a non-zero pace
// must take at least two inter-event gaps to drain. The bound is a lower
// bound, so a slow machine cannot make it flake; a build that drops the wait
// drains in microseconds and fails.
func TestConversationPaceDelaysBetweenEvents(t *testing.T) {
	const pace = 20 * time.Millisecond
	c := New(fixtureEvents(), pace)

	start := time.Now()
	handle, err := c.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var got []uievent.Event
	for ev := range handle.Events() {
		got = append(got, ev)
	}
	elapsed := time.Since(start)

	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Kind != uievent.KindTurnStart || got[2].Kind != uievent.KindTurnEnd {
		t.Errorf("paced replay changed the order: %v", got)
	}
	if elapsed < 2*pace {
		t.Errorf("paced replay finished in %v, want at least %v at pace %v", elapsed, 2*pace, pace)
	}
}
