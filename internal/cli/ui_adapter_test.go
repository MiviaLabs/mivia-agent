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
// full, non-critical published events are dropped without blocking.
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

// TestUIAdapterTurnEndNotDroppedWhenFull verifies critical lifecycle events
// still deliver when the channel is full (bounded wait until drained).
func TestUIAdapterTurnEndNotDroppedWhenFull(t *testing.T) {
	bus := events.New()
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 50 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	bus.Subscribe(events.KindToolStart, a)

	// Fill the single slot with a non-critical event.
	bus.Publish(events.NewEvent(events.KindToolStart))

	// Publish TurnEnd from another goroutine so Publish can wait until drained.
	done := make(chan struct{})
	go func() {
		bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "ok"})
		close(done)
	}()

	// Drain the non-critical event, then TurnEnd must arrive.
	msg1 := a.PollCmd()()
	if ev, ok := msg1.(uiEventMsg); !ok || ev.event.Kind != events.KindToolStart {
		t.Fatalf("expected ToolStart first, got %#v", msg1)
	}
	msg2 := a.PollCmd()()
	if ev, ok := msg2.(uiEventMsg); !ok || ev.event.Kind != events.KindTurnEnd {
		t.Fatalf("expected TurnEnd not dropped, got %#v", msg2)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TurnEnd Publish did not complete")
	}
}

// TestUIAdapterCriticalSendDoesNotHangForever: when the UI never drains,
// TurnEnd must still return (bounded wait), not pin the agent forever.
func TestUIAdapterCriticalSendDoesNotHangForever(t *testing.T) {
	a := &UIAdapter{
		evChan:  make(chan events.Event, 1),
		pollDur: 50 * time.Millisecond,
	}
	// Fill channel so send would block without a timeout.
	a.evChan <- events.NewEvent(events.KindToolStart)

	start := time.Now()
	// Use a short timeout for the test (override via temporary smaller wait by
	// calling HandleEvent; production uses criticalSendTimeout).
	done := make(chan struct{})
	go func() {
		a.HandleEvent(context.Background(), events.Event{Kind: events.KindTurnEnd, Detail: "stuck"})
		close(done)
	}()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > criticalSendTimeout+2*time.Second {
			t.Fatalf("critical send took too long: %s", elapsed)
		}
	case <-time.After(criticalSendTimeout + 3*time.Second):
		t.Fatal("critical HandleEvent hung past timeout")
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
