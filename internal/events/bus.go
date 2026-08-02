package events

import (
	"context"
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
	sub := newSubscription(b.ctx, h, bufSize)
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
// no-op. Comparison uses pointer identity (handler == target).
func (b *Bus) Unsubscribe(kind Kind, target Handler) {
	if target == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[kind]
	for i, s := range subs {
		if s.handler == target {
			s.stop()
			b.subs[kind] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

// Flush blocks until all events that were published BEFORE the Flush call
// have been delivered to their handlers. It does this by sending a barrier
// event to each active subscription and waiting for the delivery goroutine
// to process it. Safe to call concurrently with Publish.
//
// Use in tests and teardown paths where you need to guarantee handler state
// reflects all prior Publish calls.
func (b *Bus) Flush() {
	b.mu.Lock()
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
func (b *Bus) Close() {
	b.close.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		// Cancel all subscriber contexts — delivery goroutines will drain
		// remaining queued events then exit.
		b.cancel()
		b.wg.Wait()
		// Clear subscription map.
		b.mu.Lock()
		b.subs = nil
		b.mu.Unlock()
	})
}
