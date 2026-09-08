package orchestrationnotify

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestQueueAdd_MissingIDOrSessionIDIsANoOp pins queue.add's own guard,
// distinct from Publish's SessionID guard: an event with an empty ID (but a
// real SessionID) must also be dropped.
func TestQueueAdd_MissingIDOrSessionIDIsANoOp(t *testing.T) {
	q := &queue{seen: map[string]struct{}{}, wake: make(chan struct{}, 1)}
	q.add(ledger.LifecycleEvent{ID: "", SessionID: "sess-1"})
	q.add(ledger.LifecycleEvent{ID: "evt-1", SessionID: ""})
	if len(q.items) != 0 {
		t.Fatalf("items = %v, want none added for missing ID/SessionID", q.items)
	}
}

// TestQueueAdd_SeenOrderTrimsPastDoubleCapacity pins the seen-set eviction
// guard: once the dedup ledger grows past 2x capacity, its oldest entry is
// evicted so a long-lived session's dedup memory stays bounded.
func TestQueueAdd_SeenOrderTrimsPastDoubleCapacity(t *testing.T) {
	q := &queue{seen: map[string]struct{}{}, wake: make(chan struct{}, 1)}
	for i := 0; i < capacity*2+5; i++ {
		q.add(ledger.LifecycleEvent{ID: string(rune('a'+i%26)) + string(rune(i)), SessionID: "sess-1"})
	}
	if len(q.seenOrder) != capacity*2 {
		t.Fatalf("seenOrder length = %d, want it capped at %d", len(q.seenOrder), capacity*2)
	}
}

// TestPublicAPI_EmptySessionIDGuards pins the empty-sessionID guard shared
// by Drain, Interrupt, and Pending.
func TestPublicAPI_EmptySessionIDGuards(t *testing.T) {
	if got := Drain(""); got != nil {
		t.Errorf("Drain(\"\") = %v, want nil", got)
	}
	if got := Interrupt(""); got != nil {
		t.Errorf("Interrupt(\"\") = %v, want nil", got)
	}
	if Pending("") {
		t.Error("Pending(\"\") = true, want false")
	}
}

// TestPublish_EmptySessionIDIsANoOp pins Publish's own guard directly.
func TestPublish_EmptySessionIDIsANoOp(t *testing.T) {
	Publish(ledger.LifecycleEvent{ID: "evt-1", SessionID: ""})
	if Pending("") {
		t.Fatal("Publish with an empty SessionID must not register a queue")
	}
}
