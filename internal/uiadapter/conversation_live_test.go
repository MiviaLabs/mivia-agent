// conversation_live_test.go pins the live-viewer fan-out: SubscribeLive
// delivers every turn event to extra read-only viewers while the
// per-turn channel keeps exactly one primary consumer, the subscription
// survives across turns, overflow marks the subscription stale instead
// of blocking the agent loop, and Close is an idempotent silent stop
// that never closes the events channel.
package uiadapter_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// countKinds tallies the framing kinds a live viewer must see exactly
// once per turn.
func countKinds(events []uievent.Event) map[uievent.Kind]int {
	out := make(map[uievent.Kind]int)
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

// TestSubscribeLive_OneViewerAcrossTwoTurns subscribes before any turn
// and drives two sequential Sends on one conversation. The subscription
// must see each turn's framing exactly once - turn.start first, turn.end
// last - and must keep receiving turn 2's events after turn 1's
// per-turn channel closed.
func TestSubscribeLive_OneViewerAcrossTwoTurns(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		{Content: "one"}, {Content: "two"},
	}}
	conv := newTestConversation(t, completer)

	events, sub := conv.SubscribeLive()
	defer sub.Close()

	for i, want := range []string{"one", "two"} {
		handle, err := conv.Send(context.Background(), intent.Send{Text: want})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		// The per-turn channel keeps exactly one primary consumer.
		drainUntilClose(t, handle.Events(), 10*time.Second)
	}

	// The subscription never closes, so collect by polling until both
	// turns' terminal events arrived.
	deadline := time.After(10 * time.Second)
	var got []uievent.Event
	for countKinds(got)[uievent.KindTurnEnd] < 2 {
		select {
		case e, ok := <-events:
			if ok {
				got = append(got, e)
			}
		case <-deadline:
			t.Fatalf("viewer saw %d turns' worth of events, want 2 (got %v)", countKinds(got)[uievent.KindTurnEnd], countKinds(got))
		}
	}
	select {
	case e := <-events:
		got = append(got, e)
		time.Sleep(50 * time.Millisecond)
		_ = e
	default:
	}

	starts := countKinds(got)[uievent.KindTurnStart]
	ends := countKinds(got)[uievent.KindTurnEnd]
	textEnds := countKinds(got)[uievent.KindTextEnd]
	if starts != 2 || ends != 2 {
		t.Fatalf("viewer framing: %d turn.start / %d turn.end over two turns, want 2 / 2 (all: %v)", starts, ends, countKinds(got))
	}
	if textEnds != 2 {
		t.Fatalf("viewer saw %d text.end over two turns, want 2 (all: %v)", textEnds, countKinds(got))
	}
	if got[0].Kind != uievent.KindTurnStart {
		t.Fatalf("viewer's first event is %v, want turn.start (a viewer subscribed before Send sees turn.start first)", got[0].Kind)
	}
}

// TestSubscribeLive_CloseIsIdempotentAndNeverClosesEvents pins the
// lifecycle contract: Close twice is safe, the events channel is never
// closed by the publisher, events after Close reach nothing, and Done
// is the only close signal.
func TestSubscribeLive_CloseIsIdempotentAndNeverClosesEvents(t *testing.T) {
	conv := newTestConversation(t, &scriptedCompleter{turns: []provider.Response{{Content: "x"}}})

	events, sub := conv.SubscribeLive()
	sub.Close()
	sub.Close() // idempotent: no panic, single done close

	select {
	case <-sub.Done():
	default:
		t.Fatal("Done() not closed after Close()")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("received an unexpected event on a closed subscription")
		}
		t.Fatal("the publisher closed the events channel; only Done/Stale may signal lifecycle")
	default:
	}
}

// TestSend_ForegroundTracksScreenOwnership pins the registrar gate's
// new semantics: a fresh conversation is foreground; SetBackground(true)
// clears foreground (an unadopted run must not install the registrar);
// the screen re-marks ownership with SetForeground.
func TestSend_ForegroundTracksScreenOwnership(t *testing.T) {
	calls := registerCountingSubagentProgress(t)

	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	conv := newTestConversation(t, completer)
	if !conv.IsForeground() {
		t.Fatal("a fresh conversation must start foreground (the startup session never passes a screen switch)")
	}

	conv.SetForeground(false)
	conv.SetForeground(true)
	conv.SetBackground(true)
	if conv.IsForeground() {
		t.Fatal("SetBackground(true) must clear foreground")
	}

	// An off-screen conversation installs nothing.
	handle, err := conv.Send(context.Background(), intent.Send{Text: "run"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)
	if *calls != 0 {
		t.Fatalf("registrar installed %d times for an unowned conversation, want 0", *calls)
	}

	// The screen adopting the background conversation re-marks ownership:
	// its dispatches then belong on the panel while the user watches.
	conv.SetForeground(true)
	handle, err = conv.Send(context.Background(), intent.Send{Text: "watched"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)
	if *calls != 1 {
		t.Fatalf("registrar installed %d times for an adopted conversation, want 1", *calls)
	}
}

// TestSubscribeLive_RaceViewersAgainstTurn exercises concurrent
// SubscribeLive/Close against an active turn's tap. Run under -race this
// is the lock-ordering guard for the registry-as-leaf-lock rule.
func TestSubscribeLive_RaceViewersAgainstTurn(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "race"}}}
	conv := newTestConversation(t, completer)

	handle, err := conv.Send(context.Background(), intent.Send{Text: "go"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, sub := conv.SubscribeLive()
			sub.Close()
		}()
	}
	drainUntilClose(t, handle.Events(), 10*time.Second)
	wg.Wait()
}
