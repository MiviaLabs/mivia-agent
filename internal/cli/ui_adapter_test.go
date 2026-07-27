package cli

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestUIAdapterPollCmdReturnsMsg verifies that PollCmd returns a non-nil
// tea.Cmd and that executing it produces either uiEventMsg or uiTickMsg.
func TestUIAdapterPollCmdReturnsMsg(t *testing.T) {
	bus := events.New()
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
	a := NewUIAdapter(bus, nil)

	// Publish on the bus.
	bus.Publish(events.NewEvent(events.KindToolStart))

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
	a := NewUIAdapter(bus, nil)

	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.NewEvent(events.KindToolEnd))
	bus.Publish(events.NewEvent(events.KindAssistant))

	expected := []events.Kind{events.KindToolStart, events.KindToolEnd, events.KindAssistant}
	for _, want := range expected {
		cmd := a.PollCmd()
		msg := cmd()
		evMsg, ok := msg.(uiEventMsg)
		if !ok {
			t.Fatalf("expected uiEventMsg for %s, got %T", want, msg)
		}
		if evMsg.event.Kind != want {
			t.Fatalf("expected kind %s, got %s", want, evMsg.event.Kind)
		}
	}
}

// TestUIAdapterDropsOnFullChannel verifies backpressure: when the channel is
// full, published events are dropped without blocking.
func TestUIAdapterDropsOnFullChannel(t *testing.T) {
	bus := events.New()
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
	// This one should be dropped (channel full).
	bus.Publish(events.NewEvent(events.KindAssistant))

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

// TestUIAdapterPollCmdContextCancelled verifies that the event bus handler
// correctly forwards events even when context is background.
func TestUIAdapterHandleEventNonBlocking(t *testing.T) {
	bus := events.New()
	a := NewUIAdapter(bus, nil)

	// HandleEvent should not block.
	a.HandleEvent(context.Background(), events.NewEvent(events.KindError))

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
