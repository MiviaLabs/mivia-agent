package events

import (
	"context"
	"sync"
)

// Bus is a synchronous, in-process event bus. Publishers call Publish and
// all subscribed handlers are called inline (same goroutine). Handlers that
// need async behaviour should buffer internally.
//
// Bus is safe for concurrent use. All exported methods are goroutine-safe.
type Bus struct {
	mu     sync.RWMutex
	subs   map[Kind][]Handler
	closed bool
	close  sync.Once
}

// New creates a new empty Bus.
func New() *Bus {
	return &Bus{
		subs: make(map[Kind][]Handler),
	}
}

// Publish delivers an event to all handlers subscribed to the event's Kind.
// Handlers are called synchronously in subscription order.
// Publishing on a closed Bus is a no-op (safe).
func (b *Bus) Publish(ev Event) {
	b.mu.RLock()
	handlers := b.subs[ev.Kind]
	b.mu.RUnlock()
	for _, h := range handlers {
		h.HandleEvent(context.Background(), ev)
	}
}

// Subscribe registers a handler for the given event Kind.
// Subscribing a nil handler is a no-op.
func (b *Bus) Subscribe(kind Kind, h Handler) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.subs[kind] = append(b.subs[kind], h)
}

// SubscribeMany registers a handler for multiple event Kinds at once.
func (b *Bus) SubscribeMany(kinds []Kind, h Handler) {
	for _, k := range kinds {
		b.Subscribe(k, h)
	}
}

// Unsubscribe removes a specific handler from the given Kind's subscriber list.
// If the handler was never subscribed, this is a no-op.
// Comparison uses pointer identity (handler == target).
func (b *Bus) Unsubscribe(kind Kind, target Handler) {
	if target == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	handlers := b.subs[kind]
	for i, h := range handlers {
		if h == target {
			b.subs[kind] = append(handlers[:i], handlers[i+1:]...)
			return
		}
	}
}

// Close marks the bus as closed. After Close, Subscribe is a no-op.
// Close is idempotent and safe to call multiple times.
func (b *Bus) Close() {
	b.close.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.closed = true
		b.subs = nil
	})
}
