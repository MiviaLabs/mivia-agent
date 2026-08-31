package events

import (
	"context"
	"sync"
)

// deliveryKey is the unexported context key type that carries the re-entrant
// Delivery ticket for the handler currently running on a subscription.
type deliveryKey struct{}

// withDelivery attaches d to ctx, so the handler running for a subscription
// can recover its own re-entrant Delivery handle via DeliveryFrom.
func withDelivery(ctx context.Context, d *Delivery) context.Context {
	return context.WithValue(ctx, deliveryKey{}, d)
}

// Delivery is the re-entrant handle a handler uses to manage its own
// subscription from inside HandleEvent. Obtain it with DeliveryFrom(ctx),
// using the context the Bus passes to the handler.
//
// Calling Bus.Unsubscribe, Bus.Flush, or Bus.Close directly from a handler
// deadlocks: each waits on the delivery goroutine that is running the
// handler, and that goroutine cannot make progress until the handler
// returns. Delivery methods are safe from inside the handler because they
// never wait on the caller's own delivery goroutine.
type Delivery struct {
	b *Bus
	s *subscription
}

// DeliveryFrom returns the Delivery ticket for the handler currently running.
// It reports ok=false when ctx does not carry a ticket (the caller is not
// inside HandleEvent on a live subscription, or the context comes from
// elsewhere).
func DeliveryFrom(ctx context.Context) (*Delivery, bool) {
	d, ok := ctx.Value(deliveryKey{}).(*Delivery)
	return d, ok
}

// Unsubscribe removes the caller's own subscription from the bus and stops
// its delivery goroutine without waiting for it. Joining here would deadlock:
// the goroutine is currently running this handler. The subscription is marked
// stopped and its context is cancelled. Once the handler returns, the
// delivery goroutine exits at the top of its loop, without re-invoking the
// handler for events queued behind the current one.
func (d *Delivery) Unsubscribe() {
	s := d.s
	s.stopped.Store(true)
	s.cancel()

	b := d.b
	b.mu.Lock()
	// Remove from every kind. Returning after the first match left a
	// subscription spanning kinds registered under the rest, with a delivery
	// goroutine already cancelled.
	b.removeSubLocked(s)
	b.mu.Unlock()
}

// Flush blocks until all events published before the call have been
// delivered to every subscription OTHER than the caller's own. The caller's
// subscription is skipped: its delivery goroutine is running this handler, so
// a flush barrier for it could never be acknowledged until the handler
// returns.
func (d *Delivery) Flush() {
	b := d.b
	b.mu.Lock()
	var others []*subscription
	for _, subs := range b.subs {
		for _, s := range subs {
			if s != d.s {
				others = append(others, s)
			}
		}
	}
	b.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range others {
		wg.Add(1)
		go func(sub *subscription) {
			defer wg.Done()
			sub.flushSend()
		}(s)
	}
	wg.Wait()
}

// Close shuts the bus down from inside a handler. It marks the bus closed,
// cancels the shutdown context, and clears the subscription map. It then
// waits for every OTHER subscription's delivery goroutine that is NOT
// currently running a handler to drain and exit. It does NOT wait for the
// caller's own goroutine: that goroutine is running this handler and exits on
// its own once the handler returns (its context is cancelled, so it drains
// and terminates).
//
// Delivery.Close NEVER waits on a subscription whose delivery goroutine is
// currently running a handler, and never waits on one whose goroutine starts
// delivering after the delivering check: in both cases its done channel
// cannot close until the handler returns. A delivering goroutine may itself
// be parked inside a concurrent close (waiting on the shared sync.Once.Do
// until the first close body returns) or inside a Delivery.Flush that
// barriers this caller's own subscription, so waiting on its done channel
// would deadlock; it drains and exits on its own once its handler returns, so
// skipping it loses no liveness. The wait is TOCTOU-free: under deliveringMu
// Close snapshots the subscription's deliveringChange channel (closed on
// every delivering false->true transition in handle()), re-checks
// delivering, then selects on {done, changed} — the moment the goroutine
// starts running a handler, the select abandons the wait. The shared Once
// body itself only marks the bus closed and cancels the shutdown context: it
// must not wait or mutate b.subs, because a concurrent close attempt parks
// inside Do until that body returns.
func (d *Delivery) Close() {
	b := d.b
	b.close.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		b.cancel()
	})

	// Collect every subscription OTHER than the caller's own. The caller's
	// goroutine is running this handler and exits on its own once the handler
	// returns.
	b.mu.Lock()
	var others []*subscription
	for _, subs := range b.subs {
		for _, s := range subs {
			if s != d.s {
				others = append(others, s)
			}
		}
	}
	b.mu.Unlock()

	// Wait for each other subscription's delivery goroutine to drain and exit,
	// unless it is currently running a handler (delivering): a delivering
	// goroutine may be parked inside a concurrent close on behalf of its
	// handler, so waiting on its done channel could deadlock. It exits on its
	// own after that handler returns.
	//
	// The delivering check is TOCTOU-free. Under deliveringMu we snapshot the
	// subscription's deliveringChange channel (closed and replaced on every
	// delivering false->true transition in handle(), before the handler runs)
	// and then re-check delivering. If the goroutine is idle at the check we
	// wait on either done (it exited while idle — safe) or the snapshotted
	// change channel: the moment the goroutine starts running a handler the
	// select abandons the wait, because that handler may wait on this caller
	// via a concurrent close or a Delivery.Flush barriering this caller's
	// subscription. It drains and exits on its own once its handler returns,
	// so Delivery.Close never waits on a subscription whose goroutine starts
	// delivering after the check.
	for _, s := range others {
		s.deliveringMu.Lock()
		changed := s.deliveringChange
		s.deliveringMu.Unlock()
		if s.delivering.Load() {
			continue
		}
		select {
		case <-s.done:
			// Goroutine exited while idle: nothing left to wait for.
		case <-changed:
			// Goroutine started running a handler while we waited: it exits
			// on its own once the handler returns, so never wait on it.
		}
	}

	b.mu.Lock()
	b.subs = nil
	b.mu.Unlock()
}
