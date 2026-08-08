package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// White-box tests for the per-subscriber delivery machinery. The paths below
// are reachable only from inside the package: a closed event channel, a flush
// barrier that arrives while draining, and the drop-oldest retry.

type countingHandler struct{ calls atomic.Int64 }

func (h *countingHandler) HandleEvent(context.Context, Event) { h.calls.Add(1) }

func TestNewSubscriptionDefaultsTheBufferSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newSubscription(ctx, nil, &countingHandler{}, 0)
	defer s.stop()

	if got := cap(s.ch); got != defaultBufSize {
		t.Fatalf("buffer cap=%d want %d - a non-positive size must fall back to the default", got, defaultBufSize)
	}
}

func TestDeliverExitsWhenTheEventChannelCloses(t *testing.T) {
	h := &countingHandler{}
	s := &subscription{
		handler: h,
		ch:      make(chan Event, 2),
		done:    make(chan struct{}),
		flushCh: make(chan chan struct{}, 1),
	}
	s.ch <- NewEvent(KindStep)
	close(s.ch)

	s.deliver(context.Background()) // returns rather than spinning on a closed channel

	if h.calls.Load() != 1 {
		t.Fatalf("queued event handled %d times, want 1", h.calls.Load())
	}
	select {
	case <-s.done:
	default:
		t.Fatal("deliver must close done on the way out")
	}
}

func TestDrainStopsOnAClosedChannel(t *testing.T) {
	h := &countingHandler{}
	s := &subscription{handler: h, ch: make(chan Event, 2), flushCh: make(chan chan struct{}, 1)}
	s.ch <- NewEvent(KindStep)
	s.ch <- NewEvent(KindStep)
	close(s.ch)

	s.drain(context.Background())

	if h.calls.Load() != 2 {
		t.Fatalf("drained %d events, want 2", h.calls.Load())
	}
}

func TestDrainAcknowledgesAFlushBarrier(t *testing.T) {
	// A Flush() racing a drain must still be answered, or Flush blocks forever.
	s := &subscription{handler: &countingHandler{}, ch: make(chan Event), flushCh: make(chan chan struct{}, 1)}
	reply := make(chan struct{})
	s.flushCh <- reply

	s.drain(context.Background())

	select {
	case <-reply:
	default:
		t.Fatal("drain left a flush barrier unacknowledged")
	}
}

func TestTrySendDropsOldestWhenTheQueueIsFull(t *testing.T) {
	s := &subscription{handler: &countingHandler{}, ch: make(chan Event, 1), flushCh: make(chan chan struct{}, 1)}
	first := NewEvent(KindStep)
	first.Detail = "oldest"
	s.trySend(first)

	newest := NewEvent(KindStep)
	newest.Detail = "newest"
	s.trySend(newest)

	if s.Drops() != 1 {
		t.Fatalf("drops=%d want 1", s.Drops())
	}
	got := <-s.ch
	if got.Detail != "newest" {
		t.Fatalf("queue kept %q, want the newest event", got.Detail)
	}
}

func TestDropOldestReportsAnEmptyQueue(t *testing.T) {
	// The race trySend guards against: it found the queue full, then a
	// consumer emptied it before the drop. Nothing is dropped and the send is
	// retried instead.
	s := &subscription{handler: &countingHandler{}, ch: make(chan Event, 1), flushCh: make(chan chan struct{}, 1)}

	if s.dropOldest() {
		t.Fatal("dropped from an empty queue")
	}
	if s.Drops() != 0 {
		t.Fatalf("drops=%d - losing the race to a consumer is not a drop", s.Drops())
	}

	s.ch <- NewEvent(KindStep)
	if !s.dropOldest() {
		t.Fatal("a queued event should have been dropped")
	}
	if s.Drops() != 1 {
		t.Fatalf("drops=%d want 1", s.Drops())
	}
}

func TestFlushSendReturnsWhenTheGoroutineHasExited(t *testing.T) {
	// Flushing a stopped subscription must not block on a reply that nobody is
	// left to send. flushCh is buffered, so the barrier send succeeds even
	// after the delivery goroutine is gone; Go picks randomly among ready
	// select cases, so this used to hang about half the time.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newSubscription(ctx, nil, &countingHandler{}, 1)
	s.stop()

	assertFlushSendReturns(t, s)
}

func TestFlushSendTakesTheExitCaseWhenABarrierIsAlreadyQueued(t *testing.T) {
	// Same exit, reached deterministically: with the single barrier slot
	// occupied the send case can never be ready, so only the exit case is.
	s := &subscription{
		handler: &countingHandler{},
		ch:      make(chan Event, 1),
		done:    make(chan struct{}),
		flushCh: make(chan chan struct{}, 1),
	}
	s.flushCh <- make(chan struct{})
	close(s.done)

	assertFlushSendReturns(t, s)
}

func assertFlushSendReturns(t *testing.T, s *subscription) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		s.flushSend()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("flushSend blocked after the delivery goroutine exited")
	}
}

// panicThenCountHandler panics on its first call, then counts normally. Used
// to prove handle() contains a handler panic without losing later events.
type panicThenCountHandler struct{ calls atomic.Int64 }

func (h *panicThenCountHandler) HandleEvent(context.Context, Event) {
	if h.calls.Add(1) == 1 {
		panic("handler panic")
	}
}

// A handler panic must not kill the process from the delivery goroutine or
// wedge the subscription: handle() recovers, the goroutine survives, and
// events queued after the panicking one are still delivered.
func TestHandlerPanicIsContainedAndDeliveryContinues(t *testing.T) {
	h := &panicThenCountHandler{}
	s := newSubscription(context.Background(), nil, h, 4)
	defer s.stop()

	s.ch <- NewEvent(KindStep) // first delivery panics
	s.ch <- NewEvent(KindStep) // must still be delivered

	deadline := time.After(2 * time.Second)
	for h.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("delivery stalled after a contained handler panic")
		case <-time.After(time.Millisecond):
		}
	}
	if got := s.Panics(); got != 1 {
		t.Fatalf("Panics = %d, want 1 (exactly the one panicking event)", got)
	}
}

// The drain paths must contain panics too: a panicking handler during a drain
// must not abort the drain or the process, and later queued events still run.
func TestDrainContainsHandlerPanics(t *testing.T) {
	h := &panicThenCountHandler{}
	s := &subscription{handler: h, ch: make(chan Event, 4), flushCh: make(chan chan struct{}, 1)}
	s.ch <- NewEvent(KindStep) // panics
	s.ch <- NewEvent(KindStep) // survives

	s.drain(context.Background())

	if got := h.calls.Load(); got != 2 {
		t.Fatalf("handler calls = %d, want 2 (drain aborted on the panic?)", got)
	}
	if got := s.Panics(); got != 1 {
		t.Fatalf("Panics = %d, want 1", got)
	}
}

// Regression: subscription.drain (the ctx.Done teardown path) must not ack a
// queued flush barrier while events remain buffered in the event channel. The
// old drain select had case reply := <-s.flushCh: close(reply) beside the
// event case, so when a Flush() raced a Close() the barrier was acked as soon
// as the random select reached it: Flush() returned before events published
// before it were delivered. drain must drain the event channel to empty
// before acking a barrier (mirroring drainEvents), the same ordering the
// deliver-loop flush path already fixes (TestFlushWaitsForEventsPublishedBeforeIt).
//
// The pre-fix drain acks a queued barrier with probability ~1/2 per select
// while events are queued, so a single run is flaky; the 50-iteration loop
// makes the failure effectively certain. The handler blocks on a goAhead
// channel for every event (deterministic synchronization, no sleeps), so the
// drain is held mid-drain while the barrier waits. When a barrier is acked
// the handler is either blocked (stable count) or all events are done, so
// reading the count at ack-receipt time detects a barrier acked while events
// were still queued.
func TestDrainDoesNotAckFlushBarrierWhileEventsQueued(t *testing.T) {
	const (
		events     = 3
		iterations = 50
	)
	for i := 0; i < iterations; i++ {
		var handled atomic.Int64
		goAhead := make(chan struct{})
		entered := make(chan struct{}, events)
		s := &subscription{
			handler: HandlerFunc(func(ctx context.Context, ev Event) {
				handled.Add(1)
				entered <- struct{}{}
				<-goAhead
			}),
			ch:      make(chan Event, events),
			done:    make(chan struct{}),
			flushCh: make(chan chan struct{}, 1),
		}
		ctx, cancel := context.WithCancel(context.Background())
		for j := 0; j < events; j++ {
			s.ch <- NewEvent(KindStep)
		}
		barrier := make(chan struct{})
		s.flushCh <- barrier

		acked := make(chan struct{}, 1)
		go func() { <-barrier; acked <- struct{}{} }()

		drained := make(chan struct{})
		go func() {
			s.drain(ctx)
			close(drained)
		}()

		released := 0
		for released < events {
			select {
			case <-acked:
				// A barrier was acked. It must only happen once the event
				// channel is empty. The handler is either blocked (stable
				// count) or everything is done, so this is exact. The pre-fix
				// drain acks as soon as its random select reaches the barrier
				// with events still queued.
				if got := handled.Load(); got != events {
					cancel()
					t.Fatalf("iter %d: drain acked a flush barrier with %d/%d events handled", i, got, events)
				}
			case <-entered:
				goAhead <- struct{}{}
				released++
			}
		}
		// Every queued event is handled: the barrier must ack and the drain
		// must exit.
		<-acked
		<-drained
		cancel()
	}
}

// Regression: subscription.drain and drainEvents must honor the re-entrant
// stopped flag like deliver() does. A handler that self-unsubscribes via
// DeliveryFrom(ctx).Unsubscribe while the bus is closing must not be
// re-invoked for events queued behind the current one - the documented
// Delivery.Unsubscribe contract. The pre-fix drain and drainEvents had no
// stopped check, so the close-drain re-invoked the handler for every event
// still queued behind the self-unsubscribe.
func TestDrainHonorsStoppedAfterSelfUnsubscribe(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	var calls atomic.Int64
	var unsubscribed atomic.Bool
	h := HandlerFunc(func(ctx context.Context, ev Event) {
		if calls.Add(1) == 1 {
			d, ok := DeliveryFrom(ctx)
			if !ok {
				return
			}
			d.Unsubscribe()
			unsubscribed.Store(true)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &subscription{
		handler: h,
		ch:      make(chan Event, 4),
		done:    make(chan struct{}),
		flushCh: make(chan chan struct{}, 1),
		cancel:  cancel,
	}
	// The handler context carries the re-entrant Delivery ticket exactly like
	// deliver() builds it in newSubscription.
	drainCtx := withDelivery(ctx, &Delivery{b: bus, s: s})

	const events = 3
	for j := 0; j < events; j++ {
		s.ch <- NewEvent(KindStep)
	}

	s.drain(drainCtx)

	if !unsubscribed.Load() {
		t.Fatal("handler never self-unsubscribed via DeliveryFrom")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("self-unsubscribed handler invoked %d times during drain, want 1 (queued events must not be re-invoked)", got)
	}
}
