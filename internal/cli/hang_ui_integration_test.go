package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Integration-style UI path regressions for hang classes (bus → adapter → poll).

// TestIntegration_UIAdapter_TurnEndUnderBackpressure: full bus Publish path with
// a full channel; TurnEnd still delivers when drained within critical timeout.
func TestIntegration_UIAdapter_TurnEndUnderBackpressure(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	bus.Subscribe(events.KindToolStart, a)

	bus.Publish(events.NewEvent(events.KindToolStart))

	done := make(chan struct{})
	go func() {
		bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "turn-complete", TurnID: "t1"})
		close(done)
	}()

	// Drain filler then TurnEnd.
	msg1 := a.PollCmd()()
	if ev, ok := msg1.(uiEventMsg); !ok || ev.event.Kind != events.KindToolStart {
		t.Fatalf("first=%#v", msg1)
	}
	msg2 := a.PollCmd()()
	if ev, ok := msg2.(uiEventMsg); !ok || ev.event.Kind != events.KindTurnEnd {
		t.Fatalf("second=%#v", msg2)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish TurnEnd did not complete")
	}
}

// TestIntegration_UIAdapter_CriticalAbandonsWhenNeverDrained ensures the bus
// publisher (agent worker) is not pinned forever when the tea loop dies.
func TestIntegration_UIAdapter_CriticalAbandonsWhenNeverDrained(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	bus.Subscribe(events.KindTurnEnd, a)
	// Fill so TurnEnd must wait / abandon.
	a.evChan <- events.NewEvent(events.KindToolStart)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		bus.Publish(events.Event{Kind: events.KindTurnEnd, Detail: "orphan"})
		close(done)
	}()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > criticalSendTimeout+2*time.Second {
			t.Fatalf("took %s", elapsed)
		}
	case <-time.After(criticalSendTimeout + 3*time.Second):
		t.Fatal("Publish blocked forever on undrained TurnEnd")
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

// TestIntegration_UIAdapter_HandleEventCtxCancel still respects ctx.Done for critical.
func TestIntegration_UIAdapter_HandleEventCtxCancel(t *testing.T) {
	a := &UIAdapter{
		evChan:  make(chan events.Event, 1),
		pollDur: 20 * time.Millisecond,
	}
	a.evChan <- events.NewEvent(events.KindToolStart)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	a.HandleEvent(ctx, events.Event{Kind: events.KindError, Detail: "x"})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("canceled critical send took %s", elapsed)
	}
}
