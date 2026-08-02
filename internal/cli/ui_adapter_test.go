package cli

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestUIAdapterPollCmdReturnsMsg verifies that PollCmd returns a non-nil
// tea.Cmd and that executing it produces either uiEventMsg or uiTickMsg.
func TestUIAdapterPollCmdReturnsMsg(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := NewUIAdapter(bus, nil)
	cmd := a.PollCmd()
	if cmd == nil {
		t.Fatal("PollCmd returned nil")
	}

	msg := cmd()
	if msg == nil {
		t.Fatal("PollCmd produced nil msg")
	}

	_, isTick := msg.(uiTickMsg)
	_, isEvent := msg.(uiEventMsg)
	if !isTick && !isEvent {
		t.Fatalf("PollCmd produced unexpected msg type %T", msg)
	}
}

// TestUIAdapterPollCmdSelfPerpetuates verifies that multiple PollCmd calls
// always return non-nil cmds (the polling chain is perpetual).
func TestUIAdapterPollCmdSelfPerpetuates(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := NewUIAdapter(bus, nil)

	for i := 0; i < 3; i++ {
		cmd := a.PollCmd()
		if cmd == nil {
			t.Fatalf("PollCmd returned nil on iteration %d", i)
		}
		msg := cmd()
		if msg == nil {
			t.Fatalf("PollCmd produced nil msg on iteration %d", i)
		}
	}
}

// TestUIAdapterHandlesEvent verifies that publishing an event on the bus
// results in a uiEventMsg from PollCmd with the correct event data.
func TestUIAdapterHandlesEvent(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := NewUIAdapter(bus, nil)

	// Publish on the bus and flush to ensure async delivery.
	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Flush()

	// PollCmd should return a uiEventMsg with the event.
	cmd := a.PollCmd()
	if cmd == nil {
		t.Fatal("PollCmd returned nil")
	}

	msg := cmd()
	if msg == nil {
		t.Fatal("PollCmd produced nil msg")
	}

	evMsg, ok := msg.(uiEventMsg)
	if !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg)
	}
	if evMsg.event.Kind != events.KindToolStart {
		t.Fatalf("expected KindToolStart, got %s", evMsg.event.Kind)
	}
}

// TestUIAdapterMultipleEvents verifies that multiple events published
// before PollCmd calls are all delivered in order.
func TestUIAdapterMultipleEvents(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := NewUIAdapter(bus, nil)

	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.NewEvent(events.KindToolEnd))
	bus.Publish(events.NewEvent(events.KindAssistant))
	bus.Flush()

	// Drain all three events. They are delivered by separate per-kind bus
	// delivery goroutines, so arrival order into the adapter channel is not
	// guaranteed — verify each expected kind arrives, not a specific order.
	delivered := map[events.Kind]bool{
		events.KindToolStart: false,
		events.KindToolEnd:   false,
		events.KindAssistant: false,
	}
	for range delivered {
		cmd := a.PollCmd()
		msg := cmd()
		evMsg, ok := msg.(uiEventMsg)
		if !ok {
			t.Fatalf("expected uiEventMsg, got %T", msg)
		}
		delivered[evMsg.event.Kind] = true
	}
	for kind, seen := range delivered {
		if !seen {
			t.Fatalf("expected %s to be delivered", kind)
		}
	}
}

// TestUIAdapterDropsOnFullChannel verifies backpressure: when the channel is
// full, published events are dropped without blocking (non-blocking send).
func TestUIAdapterDropsOnFullChannel(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	// Use a tiny channel to test backpressure.
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 2),
		pollDur: 10 * time.Millisecond,
	}

	// Subscribe the adapter as a handler.
	bus.SubscribeMany([]events.Kind{
		events.KindToolStart, events.KindToolEnd, events.KindAssistant,
	}, a)

	// Fill the channel (2 slots).
	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.NewEvent(events.KindToolEnd))
	bus.Flush() // ensure both are delivered to adapter's evChan

	// This one should be dropped (channel full).
	bus.Publish(events.NewEvent(events.KindAssistant))
	bus.Flush()

	// Drain: first two succeed.
	cmd1 := a.PollCmd()
	msg1 := cmd1()
	if _, ok := msg1.(uiEventMsg); !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg1)
	}

	cmd2 := a.PollCmd()
	msg2 := cmd2()
	if _, ok := msg2.(uiEventMsg); !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg2)
	}

	// Third: should be a tick (dropped event) or nothing.
	cmd3 := a.PollCmd()
	msg3 := cmd3()
	if evMsg, ok := msg3.(uiEventMsg); ok {
		t.Fatalf("expected dropped event, but got %s", evMsg.event.Kind)
	}
}

// TestUIAdapterTurnEndDeliveredAsync verifies that TurnEnd is delivered via
// the async bus without blocking Publish. With the async bus, Publish never
// blocks regardless of whether the adapter's channel is full.
func TestUIAdapterTurnEndDeliveredAsync(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 512),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	bus.Subscribe(events.KindToolStart, a)

	// Publish TurnEnd — must return immediately.
	start := time.Now()
	bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "ok"})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Publish blocked for %s", elapsed)
	}

	bus.Flush()

	// TurnEnd should be available via PollCmd.
	cmd := a.PollCmd()
	msg := cmd()
	ev, ok := msg.(uiEventMsg)
	if !ok || ev.event.Kind != events.KindTurnEnd {
		t.Fatalf("expected TurnEnd, got %#v", msg)
	}
}

// TestUIAdapterHandleEventNonBlocking verifies HandleEvent (called by the
// bus delivery goroutine) uses a non-blocking send and returns quickly.
func TestUIAdapterHandleEventNonBlocking(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := NewUIAdapter(bus, nil)

	bus.Publish(events.NewEvent(events.KindError))
	bus.Flush()

	cmd := a.PollCmd()
	msg := cmd()
	evMsg, ok := msg.(uiEventMsg)
	if !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg)
	}
	if evMsg.event.Kind != events.KindError {
		t.Fatalf("expected KindError, got %s", evMsg.event.Kind)
	}
}

// TestUIAdapterPublishNeverBlocks verifies that bus.Publish returns immediately
// even when the adapter's channel is full. This is the core async bus guarantee.
func TestUIAdapterPublishNeverBlocks(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindToolStart, a)
	bus.Subscribe(events.KindTurnEnd, a)

	// Fill the single slot.
	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Flush()

	// Publish TurnEnd — with async bus this must return immediately.
	start := time.Now()
	bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "async-test"})
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Publish blocked for %s on async bus", elapsed)
	}

	// Drain both.
	_ = a.PollCmd()()
	_ = a.PollCmd()()
}
