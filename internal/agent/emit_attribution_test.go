package agent

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestEmit_NonZeroOriginAttributesTheBusEvent pins emit's own
// !e.Origin.IsZero() branch: an event from a subagent must carry agent
// attribution on the published bus event.
func TestEmit_NonZeroOriginAttributesTheBusEvent(t *testing.T) {
	bus := events.New()
	got := make(chan events.Event, 1)
	bus.Subscribe(events.KindToolStart, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		got <- ev
	}))

	opts := Options{EventBus: bus, SessionID: "s1", TurnID: "t1"}
	emit(opts, Event{
		Kind: EventToolStart, Name: "read_file",
		Origin: EventOrigin{TaskID: "task-1", Agent: "worker", Depth: 1},
	})

	select {
	case ev := <-got:
		if ev.AgentTask != "task-1" || ev.AgentName != "worker" || ev.AgentDepth != 1 {
			t.Fatalf("agent attribution lost: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}
}
