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
	s := newSubscription(ctx, &countingHandler{}, 0)
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
	s := newSubscription(ctx, &countingHandler{}, 1)
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
