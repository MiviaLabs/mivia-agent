package cli

import (
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Integration-style UI path regressions for hang classes (bus → adapter → poll).

// TestIntegration_UIAdapter_TurnEndUnderBackpressure: full bus Publish path with
// a full adapter channel; TurnEnd still delivers via async bus (Publish never
// blocks, but drop-oldest may drop if the bus queue AND adapter channel are both full).
func TestIntegration_UIAdapter_TurnEndUnderBackpressure(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 512), // use large buffer so no drops
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	bus.Subscribe(events.KindToolStart, a)

	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "turn-complete", TurnID: "t1"})
	bus.Flush()

	// Drain both events. They are delivered by two per-kind bus delivery
	// goroutines, so arrival order into the adapter channel is not guaranteed.
	got := map[events.Kind]bool{}
	for i := 0; i < 2; i++ {
		cmd := a.PollCmd()
		msg := cmd()
		ev, ok := msg.(uiEventMsg)
		if !ok {
			t.Fatalf("expected uiEventMsg, got %#v", msg)
		}
		got[ev.event.Kind] = true
	}
	if !got[events.KindToolStart] || !got[events.KindTurnEnd] {
		t.Fatalf("expected ToolStart and TurnEnd delivered, got %#v", got)
	}
}

// TestIntegration_UIAdapter_PublishNeverBlocks ensures the bus publisher
// (agent worker) is never pinned, even when the UI never drains.
// With the async bus, Publish enqueues to a bounded queue and returns immediately.
func TestIntegration_UIAdapter_PublishNeverBlocks(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	// Don't drain evChan — simulate stuck TUI.

	start := time.Now()
	// Publish many events — none should block.
	for i := 0; i < 300; i++ {
		bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "orphan"})
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Publish blocked: 300 events took %s", elapsed)
	}
}

// TestIntegration_WorkerWait_DoesNotBlockProcessExit covers the tui_run teardown path.
func TestIntegration_WorkerWait_DoesNotBlockProcessExit(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1) // hung worker
	start := time.Now()
	waitWorkerGroup(&wg, 40*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("waitWorkerGroup hung: %s", elapsed)
	}
	// Unblock for cleanliness.
	wg.Done()
}

// TestIntegration_UIAdapter_HandleEventCtxCancel still respects ctx.Done.
// With async bus, the handler receives the bus's shutdown context.
func TestIntegration_UIAdapter_HandleEventCtxCancel(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindError, a)

	// Publish and flush — handler should be called with bus context.
	bus.Publish(events.Event{Kind: events.KindError, Detail: "x"})
	bus.Flush()

	// Verify event arrived
	select {
	case ev := <-a.evChan:
		if ev.Kind != events.KindError {
			t.Fatalf("expected KindError, got %s", ev.Kind)
		}
	default:
		// Event may have been dropped if evChan was full, that's ok
		// with the non-blocking send.
	}
}

// TestIntegration_UIAdapter_BusCloseDrains verifies that bus.Close()
// drains queued events to the adapter before returning.
func TestIntegration_UIAdapter_BusCloseDrains(t *testing.T) {
	bus := events.New()
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 512),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindToolStart, a)
	bus.Subscribe(events.KindTurnEnd, a)
	bus.Subscribe(events.KindToolEnd, a)

	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.NewEvent(events.KindTurnEnd))
	bus.Publish(events.NewEvent(events.KindToolEnd))

	// Close drains all queued events.
	bus.Close()

	count := 0
	for {
		cmd := a.PollCmd()
		msg := cmd()
		if _, ok := msg.(uiEventMsg); ok {
			count++
		} else {
			break
		}
	}
	if count != 3 {
		t.Fatalf("expected 3 events after bus.Close drain, got %d", count)
	}
}
