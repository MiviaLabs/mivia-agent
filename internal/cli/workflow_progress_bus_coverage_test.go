package cli

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestWorkflowProgressSinkNilBusFromProvider covers workflowProgressSink's
// provider short-circuits: a provider returning a nil bus, and a nil provider,
// both disable progress publishing by yielding a nil sink.
func TestWorkflowProgressSinkNilBusFromProvider(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	e.SetEventBusProvider(func() *events.Bus { return nil })
	if sink := e.workflowProgressSink(); sink != nil {
		t.Fatalf("workflowProgressSink() = %v, want nil when the provider returns a nil bus", sink)
	}
	e.SetEventBusProvider(nil)
	if sink := e.workflowProgressSink(); sink != nil {
		t.Fatalf("workflowProgressSink() = %v, want nil for a nil provider", sink)
	}
}

// TestPublishDeliveredRunFinishedSkipsMissingOrUnsucceededDelivery covers the
// delivery-record gate of publishDeliveredRunFinished: a run settled to
// succeeded outside the controller publishes no run_finished when the
// idempotency-key read fails (no delivery record), and also none when the
// delivery record exists but is not succeeded/no_diff.
func TestPublishDeliveredRunFinishedSkipsMissingOrUnsucceededDelivery(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)
	ctx := context.Background()

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("fixture run status = %q, want delivery_pending", run.Status)
	}
	// Settle the run as succeeded with NO delivery record: the delivery
	// completed elsewhere, so the record lookup fails and nothing publishes.
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(root, config)
	e.SetEventBus(bus)
	e.publishDeliveredRunFinished(ctx, repo, runID)
	bus.Flush()
	if got := h.take(); len(got) != 0 {
		t.Fatalf("published %d events for a succeeded run with no delivery record; want none", len(got))
	}

	// Now a delivery record that is neither succeeded nor no_diff: the same
	// gate must refuse to publish the terminal event.
	run, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: delivery.DeliveryKey(runID, run.WorkflowDigest),
		Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}
	e.publishDeliveredRunFinished(ctx, repo, runID)
	bus.Flush()
	if got := h.take(); len(got) != 0 {
		t.Fatalf("published %d events for a failed delivery record; want none", len(got))
	}
}

// TestPublishCanceledAttemptsEmitsStepCompleted covers publishCanceledAttempts
// with a live bus: one step_completed(canceled) event is published per settled
// attempt, carrying the attempt identity in the metadata payload.
func TestPublishCanceledAttemptsEmitsStepCompleted(t *testing.T) {
	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(t.TempDir(), "")
	e.SetEventBus(bus)
	attempts := []workflowledger.StepAttempt{
		{AttemptID: "att-1", RunID: "wfr-cancel", StepID: "one", AttemptNo: 1, CoordinatorRunID: "cr-1", TaskID: "task-1", Status: workflowledger.AttemptStatusSucceeded},
		{AttemptID: "att-2", RunID: "wfr-cancel", StepID: "two", AttemptNo: 1, Status: workflowledger.AttemptStatusPending},
	}
	e.publishCanceledAttempts("wfr-cancel", attempts)
	bus.Flush()
	got := h.take()
	if len(got) != len(attempts) {
		t.Fatalf("events = %d, want %d", len(got), len(attempts))
	}
	for i, ev := range got {
		if ev.Kind != events.KindWorkflowStepCompleted {
			t.Fatalf("event %d kind = %s, want workflow_step_completed", i, ev.Kind)
		}
		if ev.Detail != "canceled" {
			t.Fatalf("event %d detail = %q, want canceled", i, ev.Detail)
		}
		if ev.Metadata["run_id"] != "wfr-cancel" || ev.Metadata["step"] != attempts[i].StepID {
			t.Fatalf("event %d metadata = %v, want run_id and step %q", i, ev.Metadata, attempts[i].StepID)
		}
	}
}

// TestPublishCanceledAttemptsNilSinkNoop covers the nil-sink guard: an engine
// without a bus publishes nothing, even when attempts were settled.
func TestPublishCanceledAttemptsNilSinkNoop(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	e.publishCanceledAttempts("wfr-cancel", []workflowledger.StepAttempt{
		{AttemptID: "att-1", RunID: "wfr-cancel", StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded},
	})
}
