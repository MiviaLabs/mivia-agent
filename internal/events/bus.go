package events

import (
	"context"
	"reflect"
	"sync"
)

// Bus is an asynchronous, in-process event bus. Publishers call Publish and
// events are enqueued to per-subscriber bounded queues. Each subscriber has
// a dedicated delivery goroutine that calls HandleEvent in FIFO order, so
// Publish never blocks on a handler.
//
// Overflow policy: drop-oldest. Each subscriber tracks a drop counter via
// Drops(). Handlers receive a cancellable context tied to bus shutdown.
//
// Bus is safe for concurrent use. All exported methods are goroutine-safe.
//
// Handlers must not call Unsubscribe, Flush, or Close directly from inside
// HandleEvent: those methods wait on the delivery goroutine that is running
// the handler, which deadlocks (self-join). A handler that needs to manage
// the bus from inside HandleEvent must use the Delivery handle obtained via
// DeliveryFrom(ctx).
type Bus struct {
	mu     sync.Mutex
	subs   map[Kind][]*subscription
	closed bool
	close  sync.Once
	ctx    context.Context // shutdown context, inherited by all subscribers
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks live delivery goroutines
}

// New creates a new empty Bus with a cancellable shutdown context.
func New() *Bus {
	ctx, cancel := context.WithCancel(context.Background())
	return &Bus{
		subs:   make(map[Kind][]*subscription),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Publish delivers an event to all handlers subscribed to the event's Kind.
// Events are enqueued to each subscriber's bounded queue; Publish never
// blocks on a handler. Publishing on a closed Bus is a no-op (safe).
//
// Per-subscriber ordering is preserved: each subscriber's delivery goroutine
// processes events in FIFO order from its queue.
func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := b.subs[ev.Kind]
	// Snapshot: Unsubscribe may modify the slice while we iterate.
	safe := make([]*subscription, len(subs))
	copy(safe, subs)
	b.mu.Unlock()

	for _, s := range safe {
		s.trySend(ev)
	}
}

// Subscribe registers a handler for the given event Kind with a bounded
// queue (default 256). Subscribing a nil handler is a no-op.
func (b *Bus) Subscribe(kind Kind, h Handler) {
	b.subscribe(kind, h, defaultBufSize)
}

// SubscribeMany registers a handler for multiple event Kinds at once.
func (b *Bus) SubscribeMany(kinds []Kind, h Handler) {
	for _, k := range kinds {
		b.Subscribe(k, h)
	}
}

// subscribe is the internal registration that creates a subscription with
// the given buffer size.
func (b *Bus) subscribe(kind Kind, h Handler, bufSize int) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	sub := newSubscription(b.ctx, b, h, bufSize)
	b.subs[kind] = append(b.subs[kind], sub)
	b.wg.Add(1)
	go func() {
		<-sub.done
		b.wg.Done()
	}()
}

// Unsubscribe removes a specific handler from the given Kind's subscriber
// list. It stops the handler's delivery goroutine after draining any
// remaining queued events. If the handler was never subscribed, this is a
// no-op. Comparison uses pointer identity for comparable handler types and
// function code-pointer identity for HandlerFunc (two closures of one
// literal compare equal — best effort, strictly better than a runtime panic
// on interface equality). Unsubscribe never panics, so the bus lock is
// always released even for uncomparable handler types.
//
// Unsubscribe blocks until the target's queued events have been drained and
// its delivery goroutine has exited, for every caller. Handlers must NOT call
// Unsubscribe from inside HandleEvent: joining the delivery goroutine that is
// running the handler deadlocks. Use DeliveryFrom(ctx).Unsubscribe() instead.
func (b *Bus) Unsubscribe(kind Kind, target Handler) {
	if target == nil {
		return
	}
	b.mu.Lock()
	if b.subs == nil {
		b.mu.Unlock()
		return
	}
	subs := b.subs[kind]
	for i, s := range subs {
		if sameHandler(s.handler, target) {
			b.subs[kind] = append(subs[:i], subs[i+1:]...)
			b.mu.Unlock()
			// Join outside the lock: the delivery goroutine may need to run
			// handlers that call back into the bus, and holding b.mu across
			// the join would deadlock them. stop() cancels the subscription
			// context and waits for the goroutine to drain and exit.
			s.stop()
			return
		}
	}
	b.mu.Unlock()
}

// Flush blocks until all events that were published BEFORE the Flush call
// have been delivered to their handlers. It does this by sending a barrier
// event to each active subscription and waiting for the delivery goroutine
// to process it. Safe to call concurrently with Publish.
//
// Use in tests and teardown paths where you need to guarantee handler state
// reflects all prior Publish calls.
//
// Flush blocks for as long as a handler is still running: the barrier for a
// subscription cannot be acknowledged until its delivery goroutine drains the
// events published before the barrier. Handlers must NOT call Flush from
// inside HandleEvent (the barrier waits on the handler's own goroutine, which
// cannot ack until the handler returns). Use DeliveryFrom(ctx).Flush()
// instead.
func (b *Bus) Flush() {
	b.mu.Lock()
	if b.subs == nil {
		b.mu.Unlock()
		return
	}
	seen := make(map[*subscription]struct{})
	for _, subs := range b.subs {
		for _, s := range subs {
			seen[s] = struct{}{}
		}
	}
	b.mu.Unlock()

	var wg sync.WaitGroup
	for s := range seen {
		wg.Add(1)
		go func(sub *subscription) {
			defer wg.Done()
			sub.flushSend()
		}(s)
	}
	wg.Wait()
}

// Close marks the bus as closed, cancels the shutdown context (which all
// handlers receive), and waits for all delivery goroutines to drain their
// queues and exit. After Close, Subscribe is a no-op and Publish is a
// silent no-op.
//
// Close is idempotent and safe to call multiple times.
//
// Close blocks until every delivery goroutine has exited, including one that
// is mid-handler. Handlers must NOT call Close from inside HandleEvent (the
// wait includes the handler's own goroutine, which cannot exit until the
// handler returns). Use DeliveryFrom(ctx).Close() instead.
//
// The sync.Once body only marks the bus closed and cancels the shutdown
// context; the wait for delivery goroutines runs AFTER Do returns. Running
// wg.Wait inside Do would let a concurrent caller parked inside Do (e.g. a
// handler calling Delivery.Close) hold a delivery goroutine hostage: that
// goroutine cannot exit until its handler returns, and the handler cannot
// return until Do completes.
func (b *Bus) Close() {
	b.close.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		// Cancel all subscriber contexts — delivery goroutines will drain
		// remaining queued events then exit.
		b.cancel()
	})
	// Drain semantics for every external caller: block until all delivery
	// goroutines have exited. Safe to run concurrently with another Close
	// (WaitGroup.Wait is goroutine-safe and the counter is only mutated by
	// Subscribe, which is a no-op once b.closed is set under b.mu).
	b.wg.Wait()
	// Clear subscription map.
	b.mu.Lock()
	b.subs = nil
	b.mu.Unlock()
}

// sameHandler reports whether two Handler values denote the same handler.
// Interface equality (a == b) panics at runtime when both dynamic types are
// identical and uncomparable — Go function types such as HandlerFunc are
// uncomparable, so comparing two HandlerFunc values panics. This helper never
// panics: comparable dynamic types use interface == (pointer identity for
// pointer handlers), differing dynamic types are never equal, and identical
// uncomparable function types fall back to code-pointer identity
// (reflect.Value.Pointer, which is documented to be zero iff the func value
// is nil, so nil funcs are handled without a panic). Any other uncomparable
// dynamic Kind is never equal.
func sameHandler(a, b Handler) bool {
	at := reflect.TypeOf(a)
	bt := reflect.TypeOf(b)
	if at == nil || bt == nil {
		// At least one nil interface: == never panics (no dynamic type to
		// compare) and is true only when both sides are nil.
		return a == b
	}
	if at != bt {
		return false
	}
	if at.Comparable() {
		return a == b
	}
	// Identical, uncomparable dynamic types. Function types (HandlerFunc) are
	// the reachable case in this package: compare code pointers.
	if at.Kind() == reflect.Func {
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	}
	return false
}
