package events

import (
	"context"
	"testing"
	"time"
)

// newObservabilityKinds is the const-like catalog of the workflow and
// invocation observability kinds added to the events bus. Every entry must
// exist in the const block in event.go AND in the allKnownKinds list in
// metrics.go. Add future kinds to both lists and to this catalog.
func newObservabilityKinds() []Kind {
	return []Kind{
		KindHeartbeat,
		KindWorkflowRunStarted,
		KindWorkflowStepStarted,
		KindWorkflowStepHeartbeat,
		KindWorkflowStepCompleted,
		KindWorkflowGateResult,
		KindWorkflowApprovalRequested,
		KindWorkflowRunFinished,
		KindWorkflowDeliveryStage,
		KindInvocationStarted,
		KindInvocationCompleted,
		KindInvocationRetrying,
	}
}

// TestNewKindsPresentInAllKnownKinds is a table test: every new kind
// constant must appear in allKnownKinds, or MetricsAdapter silently drops
// events of that kind. Future kinds must be added to both lists.
func TestNewKindsPresentInAllKnownKinds(t *testing.T) {
	known := knownKindsSet()
	for _, kind := range newObservabilityKinds() {
		if !known[kind] {
			t.Errorf("allKnownKinds is missing kind %q", kind)
		}
	}
}

// TestNewKindsRoundTripThroughBus verifies that every new kind delivers
// through a Bus publish/subscribe round trip. A kind that reaches
// allKnownKinds but not the bus wiring fails here.
func TestNewKindsRoundTripThroughBus(t *testing.T) {
	for _, kind := range newObservabilityKinds() {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			bus := New()
			t.Cleanup(bus.Close)
			got := make(chan Event, 1)
			bus.Subscribe(kind, HandlerFunc(func(_ context.Context, ev Event) {
				got <- ev
			}))

			bus.Publish(NewEvent(kind))

			select {
			case ev := <-got:
				if ev.Kind != kind {
					t.Fatalf("expected kind %q, got %q", kind, ev.Kind)
				}
			case <-time.After(time.Second):
				t.Fatalf("timeout waiting for kind %q", kind)
			}
		})
	}
}

// TestBusHeartbeatRoundTrip verifies that the root-loop heartbeat kind
// flows through a Bus publish/subscribe round trip. The root loop publishes
// the bare string "heartbeat"; this test locks in both the constant value
// and its delivery.
func TestBusHeartbeatRoundTrip(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &collectHandler{}
	bus.Subscribe(KindHeartbeat, h)

	bus.Publish(NewEvent(KindHeartbeat))
	bus.Flush()

	if h.Len() != 1 {
		t.Fatalf("handler received %d events, want 1", h.Len())
	}
	if got := h.Events()[0].Kind; got != KindHeartbeat {
		t.Fatalf("expected kind %q, got %q", KindHeartbeat, got)
	}
	if string(KindHeartbeat) != "heartbeat" {
		t.Fatalf("KindHeartbeat value = %q, want \"heartbeat\"", KindHeartbeat)
	}
}

// TestMetricsAdapter_CountsWorkflowAndInvocationKinds publishes one event
// of every new kind to a subscribed MetricsAdapter and asserts the total
// and each per-kind count. Fails until every new kind is added to
// allKnownKinds.
func TestMetricsAdapter_CountsWorkflowAndInvocationKinds(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	kinds := newObservabilityKinds()
	for _, kind := range kinds {
		bus.Publish(NewEvent(kind))
	}
	bus.Flush()

	counts, total := adapter.Snapshot()
	if total != uint64(len(kinds)) {
		t.Fatalf("expected total=%d, got %d", len(kinds), total)
	}
	for _, kind := range kinds {
		if counts[string(kind)] != 1 {
			t.Errorf("expected %s=1, got %d", kind, counts[string(kind)])
		}
	}
}
