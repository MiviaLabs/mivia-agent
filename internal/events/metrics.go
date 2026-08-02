package events

import (
	"context"
	"sync"
)

// MetricsAdapter collects per-kind event counts.
// Implements events.Handler. Safe for concurrent use.
// Call Subscribe() to attach to a Bus. Call Close() to detach.
type MetricsAdapter struct {
	mu              sync.RWMutex
	counts          map[Kind]uint64
	bus             *Bus
	subscribedKinds []Kind
	subscribed      bool
}

// NewMetricsAdapter creates a MetricsAdapter with empty counters.
// Does NOT subscribe. Call Subscribe() to attach to a Bus.
func NewMetricsAdapter() *MetricsAdapter {
	return &MetricsAdapter{
		counts: make(map[Kind]uint64),
	}
}

// HandleEvent implements events.Handler. Increments per-kind counter.
// Recovers from panics to avoid crashing the publisher goroutine.
func (m *MetricsAdapter) HandleEvent(ctx context.Context, ev Event) {
	// Recover from any panic in downstream handlers or within this method.
	defer func() {
		if r := recover(); r != nil {
			// Swallow panic - do not crash the publisher.
			_ = r
		}
	}()

	m.mu.Lock()
	m.counts[ev.Kind]++
	m.mu.Unlock()
}

// allKnownKinds is the event kind constants MetricsAdapter subscribes to in
// bulk. Used by Subscribe to subscribe to all kinds and by Close to
// unsubscribe.
var allKnownKinds = []Kind{
	KindAssistant, KindToolStart, KindToolEnd, KindStep, KindPrune,
	KindToolParallel, KindSubagentStart, KindSubagentEnd, KindSubagentHeartbeat,
	KindSubagentDone,
	KindSessionStart, KindSessionEnd, KindTurnStart, KindTurnEnd,
	KindUIResize, KindUserInput, KindUIReady, KindConfigChange,
	KindError, KindCacheUsage,
}

// Subscribe subscribes the adapter to all known event kinds on the given Bus.
// Idempotent - safe to call multiple times (subsequent calls are no-op).
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

// Snapshot returns a consistent snapshot of all per-kind event counts
// and the total event count across all kinds. Returns a new map on each
// call - safe to iterate.
func (m *MetricsAdapter) Snapshot() (counts map[string]uint64, totalEvents uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts = make(map[string]uint64, len(m.counts))
	for kind, n := range m.counts {
		counts[string(kind)] = n
		totalEvents += n
	}
	return
}

// Reset zeros all counters. Does NOT unsubscribe from the bus.
func (m *MetricsAdapter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts = make(map[Kind]uint64)
}

// Close unsubscribes from the bus (all subscribed kinds) and resets counters.
// Idempotent - safe to call multiple times. Safe to call after Bus.Close().
func (m *MetricsAdapter) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Unsubscribe from each kind we subscribed to.
	if m.bus != nil && m.subscribedKinds != nil {
		for _, kind := range m.subscribedKinds {
			m.bus.Unsubscribe(kind, m)
		}
	}
	m.counts = make(map[Kind]uint64)
	m.subscribed = false
	m.bus = nil
	m.subscribedKinds = nil
}

// compile-time interface check
var _ Handler = (*MetricsAdapter)(nil)
