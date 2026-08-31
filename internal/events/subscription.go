package events

import (
	"context"
	"sync"
	"sync/atomic"
)

// defaultBufSize is the per-subscriber queue capacity when Subscribe is used
// without explicit options. 256 is large enough for bursty publish patterns
// (e.g. tool_parallel, subagent_heartbeat bursts) while bounding unbounded
// memory growth from a slow consumer.
const defaultBufSize = 256

// subscription manages the async delivery channel and goroutine for one
// (Kind, Handler) pair registered on the Bus.
type subscription struct {
	handler Handler
	// kinds records every Kind this subscription is registered under, so
	// removal can span all of them. A subscription left registered under one
	// kind after its delivery goroutine stopped feeds a queue nobody drains.
	// Written once at registration, before the subscription is reachable from
	// b.subs, and read only under b.mu.
	kinds   []Kind
	ch      chan Event
	drops   atomic.Uint64 // drop-oldest counter
	panics  atomic.Uint64 // handler panics contained by handle()
	cancel  context.CancelFunc
	done    chan struct{}      // closed when delivery goroutine exits
	flushCh chan chan struct{} // dedicated channel for flush barriers
	// stopped is set by Delivery.Unsubscribe before cancel(). deliver() checks
	// it at the top of every loop iteration, so a re-entrant stop makes the
	// delivery goroutine exit promptly after the current handler returns,
	// without re-invoking the handler for queued events. Only the Delivery
	// path sets it: plain (external) Bus.Unsubscribe joins the goroutine, so
	// it never needs the flag.
	stopped atomic.Bool
	// delivering is true while the delivery goroutine is inside handle()
	// running a handler. Delivery.Close consults it to avoid waiting on a
	// subscription whose delivery goroutine is running a handler: that
	// handler may itself be parked inside a concurrent close
	// (sync.Once.Do), so waiting on its done channel would deadlock. Such a
	// goroutine drains and exits on its own once the handler returns.
	delivering atomic.Bool
	// deliveringMu serializes the delivering false->true transition with
	// Delivery.Close's snapshot of deliveringChange. It is a leaf lock: it is
	// acquired without holding b.mu and never held across the done wait, so
	// no lock-order inversion is introduced.
	deliveringMu sync.Mutex
	// deliveringChange is closed and replaced with a fresh open channel on
	// every delivering false->true transition (in handle(), before the
	// handler runs). Delivery.Close snapshots it under deliveringMu and
	// selects on it, so the moment a subscription's goroutine starts
	// delivering, Close abandons the wait instead of joining a goroutine
	// that cannot exit until its handler returns.
	deliveringChange chan struct{}
}

func newSubscription(ctx context.Context, b *Bus, h Handler, bufSize int) *subscription {
	if bufSize <= 0 {
		bufSize = defaultBufSize
	}
	s := &subscription{
		handler:          h,
		ch:               make(chan Event, bufSize),
		done:             make(chan struct{}),
		flushCh:          make(chan chan struct{}, 1), // small buffer for flush signals
		deliveringChange: make(chan struct{}),
	}
	ctx, s.cancel = context.WithCancel(ctx)
	if b != nil {
		// Attach the re-entrant Delivery ticket to the handler context:
		// handle() passes this ctx to the handler, so DeliveryFrom(ctx) can
		// recover the handle for the subscription running the handler.
		ctx = withDelivery(ctx, &Delivery{b: b, s: s})
	}
	go s.deliver(ctx)
	return s
}

// deliver reads from the subscriber's event channel and calls HandleEvent.
// Handler panics are contained by handle() so a misbehaving subscriber can
// never kill the process from this background goroutine or wedge the queue.
// It also monitors the flush channel to support the Bus.Flush() synchronization
// mechanism. It exits when ctx is cancelled.
func (s *subscription) deliver(ctx context.Context) {
	defer close(s.done)
	for {
		// A re-entrant stop (Delivery.Unsubscribe from the target's own
		// handler) marks the subscription stopped while the delivery goroutine
		// is busy in handle(). The current handler cannot be preempted, but
		// once it returns this check makes the goroutine exit promptly
		// WITHOUT re-invoking the handler for events queued behind it.
		if s.stopped.Load() {
			return
		}
		select {
		case ev, ok := <-s.ch:
			if !ok {
				return
			}
			s.handle(ctx, ev)
		case reply := <-s.flushCh:
			// Flush barrier: drain all queued events first, then
			// close the reply channel to signal completion.
			s.drainEvents(ctx)
			close(reply)
		case <-ctx.Done():
			s.drain(ctx)
			return
		}
	}
}

// drain processes all events currently buffered in the event channel, acking
// any pending flush barrier only once the channel is empty, and exits when
// the subscription is stopped or nothing is left to process. It is the
// ctx.Done() teardown path for the delivery goroutine, so it must preserve
// the same invariants as deliver()'s flush case: a re-entrant stop
// (Delivery.Unsubscribe) terminates the drain without re-invoking the
// handler, and a flush barrier is acked only after every queued event has
// been delivered (a barrier acked early lets Flush() return before events
// published before it are handled).
func (s *subscription) drain(ctx context.Context) {
	for {
		if s.stopped.Load() {
			// Re-entrant stop: the handler self-unsubscribed, so events queued
			// behind it must not be re-invoked. Ack any pending flush barrier
			// so a concurrent Flush does not wait on this exiting goroutine.
			s.ackFlushBarrier()
			return
		}
		// Drain queued events before acking any flush barrier (mirror
		// drainEvents ordering): a barrier acked while events remain queued
		// breaks the documented Flush barrier ordering.
		s.drainEvents(ctx)
		if !s.ackFlushBarrier() {
			return
		}
	}
}

// ackFlushBarrier closes one pending flush barrier, if any, and reports
// whether one was acked.
func (s *subscription) ackFlushBarrier() bool {
	select {
	case reply := <-s.flushCh:
		close(reply)
		return true
	default:
		return false
	}
}

// drainEvents processes everything currently buffered in the event channel
// and returns. Unlike drain it never touches flushCh: a flush barrier must
// only be acked once the event channel is empty, otherwise a second Flush
// whose barrier is queued mid-drain can be acked while events published
// before it are still undelivered (Flush() returning early breaks the
// documented barrier ordering). A re-entrant stop (Delivery.Unsubscribe)
// terminates the drain without re-invoking the handler for queued events.
func (s *subscription) drainEvents(ctx context.Context) {
	for {
		if s.stopped.Load() {
			return
		}
		select {
		case ev, ok := <-s.ch:
			if !ok {
				return
			}
			s.handle(ctx, ev)
		default:
			return
		}
	}
}

// handle invokes the subscriber's HandleEvent, containing any panic so the
// delivery goroutine survives and keeps processing queued events. A panicking
// handler is a subscriber bug, not a reason to take down the process or to
// silently lose every later event: the panic is counted and the goroutine
// continues. This mirrors the codebase's own expectation (MetricsAdapter
// recovers internally) and makes it apply to every handler uniformly.
func (s *subscription) handle(ctx context.Context, ev Event) {
	s.delivering.Store(true)
	// Publish the delivering false->true transition before the handler runs:
	// close the current deliveringChange channel (waking any Delivery.Close
	// that snapshotted it) and replace it with a fresh open channel. The
	// Store(true) above happens-before this close under deliveringMu, so a
	// Close that snapshots the channel and then sees delivering==false is
	// guaranteed to observe the channel the next transition closes. Lazy-init
	// keeps hand-built subscriptions (white-box tests) safe.
	s.deliveringMu.Lock()
	if s.deliveringChange == nil {
		s.deliveringChange = make(chan struct{})
	}
	close(s.deliveringChange)
	s.deliveringChange = make(chan struct{})
	s.deliveringMu.Unlock()
	defer func() {
		// Clear before the recover so a panicking handler also releases the
		// delivering flag: Delivery.Close must never wait on this goroutine
		// while it is (or was) running a handler.
		s.delivering.Store(false)
		if r := recover(); r != nil {
			s.panics.Add(1)
		}
	}()
	s.handler.HandleEvent(ctx, ev)
}

// Panics returns the number of handler panics this subscription has contained.
func (s *subscription) Panics() uint64 {
	return s.panics.Load()
}

// Drops returns the number of events dropped due to a full queue.
func (s *subscription) Drops() uint64 {
	return s.drops.Load()
}

// trySend enqueues an event to the subscriber's channel. If the channel is
// full, it drops the oldest event (receive one, discard it, then send).
func (s *subscription) trySend(ev Event) {
	for {
		select {
		case s.ch <- ev:
			return
		default:
			// Channel full: drop oldest to make room. A false return means a
			// consumer emptied it between the two selects, so there was
			// nothing to drop and the send is simply retried.
			s.dropOldest()
		}
	}
}

// dropOldest discards the oldest queued event to make room for a new one and
// reports whether it dropped anything. It is a separate method so the
// "nothing left to drop" outcome - the loser of the race with a consumer -
// can be exercised on its own instead of only through a timing window.
func (s *subscription) dropOldest() bool {
	select {
	case <-s.ch:
		s.drops.Add(1)
		return true
	default:
		return false
	}
}

// flushSend sends a flush barrier to the delivery goroutine and waits for
// acknowledgment. Unlike event sends, flush barriers use a dedicated channel
// that is never subject to drop-oldest, ensuring Flush() never deadlocks.
func (s *subscription) flushSend() {
	reply := make(chan struct{})
	select {
	case s.flushCh <- reply:
	case <-s.done:
		return
	}
	// The barrier is queued, but waiting on the reply alone would hang: flushCh
	// is buffered, so the send above also succeeds against a delivery goroutine
	// that has already exited - and Go picks randomly among ready select cases,
	// so that branch was taken about half the time and Flush() after Close()
	// blocked forever. Wait for the ack or for the goroutine to be gone.
	select {
	case <-reply:
	case <-s.done:
	}
}

// stop cancels the delivery context and waits for the goroutine to exit.
func (s *subscription) stop() {
	s.cancel()
	<-s.done
}
