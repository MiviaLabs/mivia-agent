package events

import (
	"context"
	"testing"
	"time"
)

// newObservabilityKinds is the const-like catalog of the workflow and
// invocation observability kinds added to the events bus. Every entry must
// exist in the const block in event.go.
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
