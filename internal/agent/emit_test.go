package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestEmitDualDelivery verifies that emit() delivers events to both
// OnEvent callback and EventBus subscribers.
func TestEmitDualDelivery(t *testing.T) {
	bus := events.New()

	var onEventCount atomic.Int32
	var busCount atomic.Int32

	// Subscribe to all agent event kinds on the bus.
	bus.SubscribeMany([]events.Kind{
		events.KindAssistant,
		events.KindToolStart,
		events.KindToolEnd,
		events.KindStep,
		events.KindPrune,
		events.KindToolParallel,
	}, events.HandlerFunc(func(ctx context.Context, ev events.Event) {
		busCount.Add(1)
	}))

	opts := Options{
		EventBus: bus,
		OnEvent: func(e Event) {
			onEventCount.Add(1)
		},
	}

	// Emit several events of different kinds.
	eventsToEmit := []Event{
		{Kind: EventAssistant, Name: "assistant", Content: "hello"},
		{Kind: EventToolStart, ToolCallID: "tc1", Name: "read_file"},
		{Kind: EventToolEnd, ToolCallID: "tc1", Name: "read_file", Output: "file content"},
		{Kind: EventStep, Detail: "step 1 of 3"},
		{Kind: EventPrune, Detail: "pruned 2 old messages"},
		{Kind: EventToolParallel, Name: "grep", Detail: "parallel tool execution"},
	}

	for _, e := range eventsToEmit {
		emit(opts, e)
	}

	if n := onEventCount.Load(); n != int32(len(eventsToEmit)) {
		t.Errorf("OnEvent called %d times, want %d", n, len(eventsToEmit))
	}
	if n := busCount.Load(); n != int32(len(eventsToEmit)) {
		t.Errorf("EventBus received %d events, want %d", n, len(eventsToEmit))
	}
}

// TestEmitOnlyOnEvent verifies that emit() works with only OnEvent
// set and no EventBus.
func TestEmitOnlyOnEvent(t *testing.T) {
	var count atomic.Int32
	opts := Options{
		OnEvent: func(e Event) {
			count.Add(1)
		},
	}

	emit(opts, Event{Kind: EventAssistant, Content: "hi"})
	emit(opts, Event{Kind: EventToolStart, Name: "grep"})

	if n := count.Load(); n != 2 {
		t.Errorf("OnEvent called %d times, want 2", n)
	}
}

// TestEmitOnlyEventBus verifies that emit() works with only EventBus
// set and no OnEvent.
func TestEmitOnlyEventBus(t *testing.T) {
	bus := events.New()
	var count atomic.Int32
	bus.Subscribe(events.KindAssistant, events.HandlerFunc(func(ctx context.Context, ev events.Event) {
		count.Add(1)
	}))

	opts := Options{EventBus: bus}
	emit(opts, Event{Kind: EventAssistant, Content: "via bus only"})

	if n := count.Load(); n != 1 {
		t.Errorf("EventBus received %d events, want 1", n)
	}
}

// TestEmitNilBoth verifies that emit() is safe when both
// OnEvent and EventBus are nil.
func TestEmitNilBoth(t *testing.T) {
	// Should not panic.
	emit(Options{}, Event{Kind: EventAssistant, Content: "no-op"})
}

func TestEmitPublishesTypedIdentityAndTurnBinding(t *testing.T) {
	bus := events.New()
	var got events.Event
	bus.Subscribe(events.KindAssistant, events.HandlerFunc(func(ctx context.Context, ev events.Event) { got = ev }))
	identity, err := events.NewIdentity("researcher", "workspace", "opaque-instance", 4)
	if err != nil {
		t.Fatal(err)
	}
	emit(Options{EventBus: bus, SessionID: "session", TurnID: "turn", EventIdentity: &identity}, Event{Kind: EventAssistant, Content: "answer"})
	if got.Identity == nil || *got.Identity != identity {
		t.Fatalf("identity = %#v, want %#v", got.Identity, identity)
	}
	if got.SessionID != "session" || got.TurnID != "turn" {
		t.Fatalf("turn binding = %q/%q", got.SessionID, got.TurnID)
	}
}
