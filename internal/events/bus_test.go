package events

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Handler types used in tests
// ---------------------------------------------------------------------------

// collectHandler accumulates received events into a slice for assertion.
type collectHandler struct {
	mu     sync.Mutex
	events []Event
}

func (h *collectHandler) HandleEvent(ctx context.Context, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *collectHandler) Events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, len(h.events))
	copy(out, h.events)
	return out
}

func (h *collectHandler) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

// chanHandler sends received events into a channel for blocking receive.
type chanHandler struct {
	ch chan Event
}

func (h *chanHandler) HandleEvent(ctx context.Context, ev Event) {
	select {
	case h.ch <- ev:
	default:
	}
}

// sentinelHandler panics if called — for testing kind filtering.
type sentinelHandler struct{}

func (h *sentinelHandler) HandleEvent(ctx context.Context, ev Event) {
	panic("sentinelHandler was unexpectedly called")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestBusPublishDeliversToSubscriber is the core contract:
// subscribe, publish, verify the handler receives the event.
func TestBusPublishDeliversToSubscriber(t *testing.T) {
	bus := New()
	h := &chanHandler{ch: make(chan Event, 1)}
	bus.Subscribe(KindToolStart, h)

	bus.Publish(NewEvent(KindToolStart))

	select {
	case ev := <-h.ch:
		if ev.Kind != KindToolStart {
			t.Fatalf("expected KindToolStart, got %s", ev.Kind)
		}
		if ev.Timestamp.IsZero() {
			t.Fatal("expected non-zero Timestamp")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event delivery")
	}
}

// TestBusMultipleSubscribers verifies that two handlers subscribed to the
// same kind both receive the same published event.
func TestBusMultipleSubscribers(t *testing.T) {
	bus := New()
	h1 := &collectHandler{}
	h2 := &collectHandler{}
	bus.Subscribe(KindAssistant, h1)
	bus.Subscribe(KindAssistant, h2)

	bus.Publish(NewEvent(KindAssistant))

	if h1.Len() != 1 {
		t.Fatalf("h1 received %d events, want 1", h1.Len())
	}
	if h2.Len() != 1 {
		t.Fatalf("h2 received %d events, want 1", h2.Len())
	}
	if h1.Events()[0].Kind != KindAssistant {
		t.Fatalf("h1 got kind %s", h1.Events()[0].Kind)
	}
}

// TestBusKindFiltering verifies that a subscriber to KindA does NOT receive
// events published with KindB.
func TestBusKindFiltering(t *testing.T) {
	bus := New()
	sentinel := &sentinelHandler{}
	bus.Subscribe(KindToolStart, sentinel)

	// Publishing a different kind must NOT call the sentinel handler.
	caught := &collectHandler{}
	bus.Subscribe(KindAssistant, caught)

	bus.Publish(NewEvent(KindAssistant))

	if caught.Len() != 1 {
		t.Fatalf("caught handler received %d events, want 1", caught.Len())
	}
}

// TestBusUnsubscribe verifies that after unsubscribing, the handler is no
// longer called.
func TestBusUnsubscribe(t *testing.T) {
	bus := New()
	h := &collectHandler{}
	bus.Subscribe(KindToolEnd, h)
	bus.Unsubscribe(KindToolEnd, h)

	bus.Publish(NewEvent(KindToolEnd))

	if h.Len() != 0 {
		t.Fatal("handler was still called after Unsubscribe")
	}
}

// TestBusCloseMultipleTimes verifies that Close() is idempotent and safe
// to call multiple times (no panic, no deadlock).
func TestBusCloseMultipleTimes(t *testing.T) {
	bus := New()
	bus.Close()
	bus.Close() // must not panic

	// After close, Publish must still be safe (no panic on closed bus).
	bus.Publish(NewEvent(KindError))
}

// TestBusSubscribeMany verifies that SubscribeMany subscribes to multiple
// kinds with a single call and all receive events.
func TestBusSubscribeMany(t *testing.T) {
	bus := New()
	h := &collectHandler{}
	kinds := []Kind{KindToolStart, KindToolEnd, KindStep}
	bus.SubscribeMany(kinds, h)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Publish(NewEvent(KindStep))

	if h.Len() != 3 {
		t.Fatalf("handler received %d events, want 3", h.Len())
	}
	got := h.Events()
	for i, k := range kinds {
		if got[i].Kind != k {
			t.Fatalf("event %d: expected kind %s, got %s", i, k, got[i].Kind)
		}
	}
}

// TestHandlerFuncAdapter verifies that HandlerFunc works as a Handler adapter.
func TestHandlerFuncAdapter(t *testing.T) {
	bus := New()
	got := make(chan Event, 1)
	bus.Subscribe(KindUIResize, HandlerFunc(func(ctx context.Context, ev Event) {
		got <- ev
	}))

	bus.Publish(NewEvent(KindUIResize))

	select {
	case ev := <-got:
		if ev.Kind != KindUIResize {
			t.Fatalf("expected KindUIResize, got %s", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for HandlerFunc delivery")
	}
}

// TestEventConstruction verifies that NewEvent sets Kind and a non-zero Timestamp.
func TestEventConstruction(t *testing.T) {
	ev := NewEvent(KindTurnStart)
	if ev.Kind != KindTurnStart {
		t.Fatalf("expected KindTurnStart, got %s", ev.Kind)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("NewEvent must set a non-zero Timestamp")
	}
	// Verify it's recent (within 5 seconds)
	if time.Since(ev.Timestamp) > 5*time.Second {
		t.Fatal("NewEvent Timestamp is too old")
	}
}

// TestBusPublishConcurrentSafe verifies that concurrent Publish calls from
// multiple goroutines are safe and all events are received.
func TestBusPublishConcurrentSafe(t *testing.T) {
	bus := New()
	h := &collectHandler{}
	bus.SubscribeMany([]Kind{KindToolStart, KindToolEnd, KindStep}, h)

	const numPublishers = 8
	const eventsPer = 100
	var wg sync.WaitGroup

	for i := 0; i < numPublishers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPer; j++ {
				kind := KindToolStart
				if j%2 == 0 {
					kind = KindToolEnd
				}
				if j%3 == 0 {
					kind = KindStep
				}
				bus.Publish(NewEvent(kind))
			}
		}(i)
	}
	wg.Wait()

	total := h.Len()
	expected := numPublishers * eventsPer
	if total != expected {
		t.Fatalf("handler received %d events, want %d (concurrent loss)", total, expected)
	}
}

// TestBusSubscribeConcurrentSafe verifies concurrent Subscribe/Unsubscribe
// while Publish is called, with no races.
func TestBusSubscribeConcurrentSafe(t *testing.T) {
	bus := New()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			bus.Publish(NewEvent(KindToolStart))
			bus.Publish(NewEvent(KindAssistant))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h := &collectHandler{}
			bus.Subscribe(KindToolStart, h)
			bus.Unsubscribe(KindToolStart, h)
		}
	}()

	wg.Wait()
	// No assertion on counts — must not race or deadlock.
	// The -race detector will catch data races.
}

// TestUnsubscribeNonexistent verifies that unsubscribing a handler that was
// never subscribed does not panic.
func TestUnsubscribeNonexistent(t *testing.T) {
	bus := New()
	h := &collectHandler{}
	// Must not panic
	bus.Unsubscribe(KindToolStart, h)
	bus.Unsubscribe(KindError, h)
}

// TestSubscribeNilHandler verifies that subscribing nil does not panic.
func TestSubscribeNilHandler(t *testing.T) {
	bus := New()
	bus.Subscribe(KindToolStart, nil) // must not panic
	bus.SubscribeMany([]Kind{KindToolEnd, KindStep}, nil)

	// Publishing should still work
	bus.Publish(NewEvent(KindToolStart))
}

// TestBusPublishEmptyBus verifies that Publish on an empty bus (no subscribers)
// does not panic.
func TestBusPublishEmptyBus(t *testing.T) {
	bus := New()
	bus.Publish(NewEvent(KindAssistant))
	bus.Publish(NewEvent(KindError))
}
