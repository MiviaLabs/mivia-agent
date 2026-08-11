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
	closing         bool // Close() ran: the adapter is permanently closed; Subscribe is a no-op
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
	KindAssistant, KindToolStart, KindToolEnd, KindStep, KindHeartbeat, KindPrune,
	KindToolParallel, KindSubagentStart, KindSubagentEnd, KindSubagentHeartbeat,
	KindSubagentDone, KindThinking, KindCompaction,
	KindSessionStart, KindSessionEnd, KindTurnStart, KindTurnEnd,
	KindWorkflowRunStarted, KindWorkflowStepStarted, KindWorkflowStepHeartbeat,
	KindWorkflowStepCompleted, KindWorkflowGateResult, KindWorkflowApprovalRequested,
	KindWorkflowRunFinished, KindWorkflowDeliveryStage,
	KindInvocationStarted, KindInvocationCompleted, KindInvocationRetrying,
	KindUIResize, KindUserInput, KindUIReady, KindConfigChange,
	KindError, KindCacheUsage, KindTokenUsage, KindPrefixReset,
}

// Subscribe subscribes the adapter to all known event kinds on the given Bus.
// Idempotent - safe to call multiple times (subsequent calls are no-op).
// After (or concurrently with) Close() it is a no-op, matching Bus.Close()
// semantics: Close is terminal.
// Stores subscribed kinds for Close() to unsubscribe.
func (m *MetricsAdapter) Subscribe(bus *Bus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribed || m.closing {
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
// Close is terminal, matching Bus.Close(): a Subscribe that races or follows
// Close is a permanent no-op.
//
// Unsubscribe synchronously joins the subscription's delivery goroutine
// (stop() waits for it to drain and exit). The delivery goroutine calls
// HandleEvent, which needs m.mu - so m.mu must NOT be held across the
// Unsubscribe loop, or a delivery goroutine draining queued events waits on
// m.mu forever while we wait on it: a deadlock. Snapshot the subscription
// list under the lock, unsubscribe lock-free, then re-lock to clear state.
func (m *MetricsAdapter) Close() {
	var bus *Bus
	var kinds []Kind
	m.mu.Lock()
	// Close is terminal: mark closing so a Subscribe racing the lock-free
	// unsubscribe window below becomes a no-op instead of re-registering
	// after we snapshot (which would leak live subscriptions past Close).
	m.closing = true
	bus = m.bus
	if m.subscribedKinds != nil {
		kinds = make([]Kind, len(m.subscribedKinds))
		copy(kinds, m.subscribedKinds)
	}
	m.mu.Unlock()

	if bus != nil && kinds != nil {
		for _, kind := range kinds {
			bus.Unsubscribe(kind, m)
		}
	}

	m.mu.Lock()
	m.counts = make(map[Kind]uint64)
	m.subscribed = false
	m.bus = nil
	m.subscribedKinds = nil
	// closing intentionally stays true: Close is terminal, so a Subscribe
	// that starts after this point is a no-op instead of re-registering
	// live subscriptions behind a detached adapter.
	m.mu.Unlock()
}

// compile-time interface check
var _ Handler = (*MetricsAdapter)(nil)
