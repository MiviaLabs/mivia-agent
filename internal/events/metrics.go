package events

import (
	"context"
	"sync"
	"time"
)

// MetricsAdapter collects per-kind event counts and handler timing.
// Implements events.Handler. Safe for concurrent use.
// Call Subscribe() to attach to a Bus. Call Close() to detach.
type MetricsAdapter struct {
	mu              sync.RWMutex
	counts          map[Kind]*counter
	bus             *Bus
	subscribedKinds []Kind
	subscribed      bool
}

type counter struct {
	n       uint64
	elapsed time.Duration
}

// NewMetricsAdapter creates a MetricsAdapter with empty counters.
// Does NOT subscribe. Call Subscribe() to attach to a Bus.
func NewMetricsAdapter() *MetricsAdapter {
	return &MetricsAdapter{
		counts: make(map[Kind]*counter),
	}
}

// HandleEvent implements events.Handler. Increments per-kind counter and
// accumulates handler processing time. Recovers from panics to avoid
// crashing the publisher goroutine.
func (m *MetricsAdapter) HandleEvent(ctx context.Context, ev Event) {
	// Recover from any panic in downstream handlers or within this method.
	defer func() {
		if r := recover(); r != nil {
			// Swallow panic — do not crash the publisher.
			_ = r
		}
	}()

	start := time.Now()

	m.mu.Lock()
	c, ok := m.counts[ev.Kind]
	if !ok {
		c = &counter{}
		m.counts[ev.Kind] = c
	}
	c.n++
	c.elapsed += time.Since(start)
	m.mu.Unlock()
}

// allKnownKinds is the list of all 17 event kind constants defined in event.go.
// Used by Subscribe to subscribe to all kinds and by Close to unsubscribe.
var allKnownKinds = []Kind{
	KindAssistant, KindToolStart, KindToolEnd, KindStep, KindPrune,
	KindToolParallel, KindSubagentStart, KindSubagentEnd, KindSubagentHeartbeat,
	KindSessionStart, KindSessionEnd, KindTurnStart, KindTurnEnd,
	KindUIResize, KindUserInput, KindUIReady, KindConfigChange,
	KindError,
}

// Subscribe subscribes the adapter to all known event kinds on the given Bus.
// Idempotent — safe to call multiple times (subsequent calls are no-op).
// Stores subscribed kinds for Close() to unsubscribe.
func (m *MetricsAdapter) Subscribe(bus *Bus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribed {
		return
	}
	m.bus = bus
	m.subscribedKinds = make([]Kind, len(allKnownKinds))
	copy(m.subscribedKinds, allKnownKinds)
	bus.SubscribeMany(allKnownKinds, m)
	m.subscribed = true
}

// Snapshot returns a consistent snapshot of all per-kind event counts,
// the total event count across all kinds, and the total elapsed handler
// processing time. Returns a new map on each call — safe to iterate.
func (m *MetricsAdapter) Snapshot() (counts map[string]uint64, totalEvents uint64, totalElapsed time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts = make(map[string]uint64, len(m.counts))
	for kind, c := range m.counts {
		counts[string(kind)] = c.n
		totalEvents += c.n
		totalElapsed += c.elapsed
	}
	return
}

// Reset zeros all counters. Does NOT unsubscribe from the bus.
func (m *MetricsAdapter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts = make(map[Kind]*counter)
}

// Close unsubscribes from the bus (all subscribed kinds) and resets counters.
// Idempotent — safe to call multiple times. Safe to call after Bus.Close().
func (m *MetricsAdapter) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Unsubscribe from each kind we subscribed to.
	if m.bus != nil && m.subscribedKinds != nil {
		for _, kind := range m.subscribedKinds {
			m.bus.Unsubscribe(kind, m)
		}
	}
	m.counts = make(map[Kind]*counter)
	m.subscribed = false
	m.bus = nil
	m.subscribedKinds = nil
}

// compile-time interface check
var _ Handler = (*MetricsAdapter)(nil)
