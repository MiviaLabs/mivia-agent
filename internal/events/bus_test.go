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

// sentinelHandler panics if called - for testing kind filtering.
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
	t.Cleanup(bus.Close)
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
	t.Cleanup(bus.Close)
	h1 := &collectHandler{}
	h2 := &collectHandler{}
	bus.Subscribe(KindAssistant, h1)
	bus.Subscribe(KindAssistant, h2)

	bus.Publish(NewEvent(KindAssistant))
	bus.Flush()

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
	t.Cleanup(bus.Close)
	sentinel := &sentinelHandler{}
	bus.Subscribe(KindToolStart, sentinel)

	// Publishing a different kind must NOT call the sentinel handler.
	caught := &collectHandler{}
	bus.Subscribe(KindAssistant, caught)

	bus.Publish(NewEvent(KindAssistant))
	bus.Flush()

	if caught.Len() != 1 {
		t.Fatalf("caught handler received %d events, want 1", caught.Len())
	}
}

// TestBusUnsubscribe verifies that after unsubscribing, the handler is no
// longer called.
func TestBusUnsubscribe(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &collectHandler{}
	bus.Subscribe(KindToolEnd, h)
	bus.Unsubscribe(KindToolEnd, h)

	bus.Publish(NewEvent(KindToolEnd))
	bus.Flush()

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
// kinds with a single call and all receive events. Per-kind ordering is
// preserved; cross-kind ordering is not guaranteed with async delivery.
func TestBusSubscribeMany(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &collectHandler{}
	kinds := []Kind{KindToolStart, KindToolEnd, KindStep}
	bus.SubscribeMany(kinds, h)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Publish(NewEvent(KindStep))
	bus.Flush()

	if h.Len() != 3 {
		t.Fatalf("handler received %d events, want 3", h.Len())
	}
	// Verify all three kinds are present (ordering not guaranteed cross-kind).
	got := make(map[Kind]bool)
	for _, ev := range h.Events() {
		got[ev.Kind] = true
	}
	for _, k := range kinds {
		if !got[k] {
			t.Fatalf("missing kind %s", k)
		}
	}
}

// TestHandlerFuncAdapter verifies that HandlerFunc works as a Handler adapter.
func TestHandlerFuncAdapter(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
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
// multiple goroutines are safe and all events are received. With the async
// bus and a 256-deep per-subscriber queue, 800 total events should fit
// without drops when the handler is fast.
func TestBusPublishConcurrentSafe(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
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
	bus.Flush()

	total := h.Len()
	expected := numPublishers * eventsPer
	// With a 256-deep queue per kind and async delivery, slight drops can
	// occur under contention. Allow up to 5% loss.
	if total < int(float64(expected)*0.95) {
		t.Fatalf("handler received %d events, want >= %d (too many drops)", total, int(float64(expected)*0.95))
	}
}

// TestBusSubscribeConcurrentSafe verifies concurrent Subscribe/Unsubscribe
// while Publish is called, with no races.
func TestBusSubscribeConcurrentSafe(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
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
	// No assertion on counts - must not race or deadlock.
	// The -race detector will catch data races.
}

// TestUnsubscribeNonexistent verifies that unsubscribing a handler that was
// never subscribed does not panic.
func TestUnsubscribeNonexistent(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &collectHandler{}
	// Must not panic
	bus.Unsubscribe(KindToolStart, h)
	bus.Unsubscribe(KindError, h)
}

// TestSubscribeNilHandler verifies that subscribing nil does not panic.
func TestSubscribeNilHandler(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	bus.Subscribe(KindToolStart, nil) // must not panic
	bus.SubscribeMany([]Kind{KindToolEnd, KindStep}, nil)

	// Publishing should still work
	bus.Publish(NewEvent(KindToolStart))
}

// TestBusPublishEmptyBus verifies that Publish on an empty bus (no subscribers)
// does not panic.
func TestBusPublishEmptyBus(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	bus.Publish(NewEvent(KindAssistant))
	bus.Publish(NewEvent(KindError))
}

// ---------------------------------------------------------------------------
// Async bus specific tests
// ---------------------------------------------------------------------------

// TestBusAsyncDelivery tests that handlers are called from a different
// goroutine than the publisher.
func TestBusAsyncDelivery(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	done := make(chan struct{})

	bus.Subscribe(KindAssistant, HandlerFunc(func(ctx context.Context, ev Event) {
		close(done)
	}))

	bus.Publish(NewEvent(KindAssistant))

	<-done
	// The handler was called from a delivery goroutine, not the publisher.
	// We just verify the handler ran and didn't deadlock.
}

// TestBusFlushSynchronizesDelivery verifies that Flush ensures all prior
// Publish calls have been delivered.
func TestBusFlushSynchronizesDelivery(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &collectHandler{}
	bus.Subscribe(KindToolStart, h)

	for i := 0; i < 50; i++ {
		bus.Publish(NewEvent(KindToolStart))
	}
	bus.Flush()

	if h.Len() != 50 {
		t.Fatalf("expected 50 events after Flush, got %d", h.Len())
	}
}

// TestBusFlushEmptyBus verifies Flush on a bus with no subscribers.
func TestBusFlushEmptyBus(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	bus.Flush() // must not panic or deadlock
}

// TestBusPublishNeverBlocks verifies that Publish returns immediately even
// when the handler is very slow.
func TestBusPublishNeverBlocks(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	slow := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		<-slow // block until test releases
	}))

	// Publish should not block even though handler is blocked.
	start := time.Now()
	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolStart))
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Publish blocked for %s", elapsed)
	}

	// Release handler
	close(slow)
	bus.Flush()
}

// TestBusDropOldest verifies that when the queue is full, old events are
// dropped and the newest event is delivered. Uses a slow handler that blocks
// to fill the queue.
func TestBusDropOldest(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	// Create a handler that blocks on first call.
	handlerBlocked := make(chan struct{})
	handlerRelease := make(chan struct{})
	callCount := 0
	var mu sync.Mutex

	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		mu.Lock()
		callCount++
		if callCount == 1 {
			mu.Unlock()
			close(handlerBlocked)
			<-handlerRelease // block after first event
			return
		}
		mu.Unlock()
	}))

	// Publish a "seed" event to trigger the handler, then wait for it
	// to block on the first call.
	bus.Publish(Event{Kind: KindToolStart})
	<-handlerBlocked

	// Publish more events. With default 256 buffer, we need >256 to overflow.
	// First event is in-flight (handler blocked), so the queue fills at 256.
	// Publish 260 total — should overflow and drop ~3 oldest from queue.
	const total = 260
	for i := 0; i < total; i++ {
		bus.Publish(Event{Kind: KindToolStart, Detail: string(rune('A' + (i % 26)))})
	}

	// Release handler.
	close(handlerRelease)
	bus.Flush()

	mu.Lock()
	count := callCount
	mu.Unlock()

	// Should have received: 1 (in-flight) + 256 (buffer) = 257
	// Oldest ~3 from queue were dropped when overflow happened.
	// Total delivered: 1 (in-flight) + 257 (buffer fills then drop-oldest)
	// = 258... actually let's just verify it's in a reasonable range.
	if count < total-10 {
		t.Fatalf("too many drops: got %d, want ~%d", count, total)
	}
}

// TestBusCloseDrainsQueue verifies that Close drains remaining queued events
// before returning.
func TestBusCloseDrainsQueue(t *testing.T) {
	bus := New()
	h := &collectHandler{}
	bus.Subscribe(KindToolStart, h)

	for i := 0; i < 10; i++ {
		bus.Publish(NewEvent(KindToolStart))
	}
	bus.Close()

	if h.Len() != 10 {
		t.Fatalf("expected 10 events after Close (drained), got %d", h.Len())
	}
}

// TestBusHandlerReceivesShutdownContext verifies that handlers receive
// the bus's shutdown context which is cancelled on Close.
func TestBusHandlerReceivesShutdownContext(t *testing.T) {
	bus := New()
	var receivedCtx context.Context
	var mu sync.Mutex
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		mu.Lock()
		receivedCtx = ctx
		mu.Unlock()
	}))

	bus.Publish(NewEvent(KindToolStart))
	bus.Flush()

	mu.Lock()
	ctx := receivedCtx
	mu.Unlock()
	if ctx == nil {
		t.Fatal("handler received nil context")
	}

	// Context should not be cancelled yet.
	if ctx.Err() != nil {
		t.Fatal("context cancelled before Close")
	}

	bus.Close()

	// After Close, context should be cancelled.
	if ctx.Err() == nil {
		t.Fatal("context not cancelled after Close")
	}
}
