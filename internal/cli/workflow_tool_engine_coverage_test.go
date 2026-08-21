package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestCoverageSetEventBusProviderNilReceiverSafe covers the nil-receiver guard
// in SetEventBusProvider: a nil engine must accept the provider silently.
func TestCoverageSetEventBusProviderNilReceiverSafe(t *testing.T) {
	var e *sessionWorkflowEngine
	e.SetEventBusProvider(func() *events.Bus { return nil })
	// SetEventBus wraps the same provider path.
	e.SetEventBus(nil)
}

// TestCoverageAttachWorkflowProgressBusNilControllerSafe covers the nil
// controller guard in attachWorkflowProgressBus: attaching a nil controller is
// a no-op even when a bus is wired.
func TestCoverageAttachWorkflowProgressBusNilControllerSafe(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	bus := events.New()
	t.Cleanup(bus.Close)
	e.SetEventBus(bus)
	e.attachWorkflowProgressBus(nil)
}

// TestCoverageAttachWorkflowProgressBusNilSinkNoop covers the nil-sink guard
// in attachWorkflowProgressBus: with no event bus provider the sink is nil and
// the attach is a no-op.
func TestCoverageAttachWorkflowProgressBusNilSinkNoop(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	e.attachWorkflowProgressBus(&controller.LinearController{})
}

// TestCoverageSessionEngineCancelPublishesRunFinished drives the full
// operator-cancel path of the session engine against a run parked at
// waiting_approval: CancelRunWithAttempts settles the run to canceled, and the
// engine publishes one step_completed(canceled) per settled attempt plus the
// run-level workflow_run_finished event the TUI and metrics consume.
func TestCoverageSessionEngineCancelPublishesRunFinished(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(root, configPath)
	e.SetEventBus(bus)

	result, err := e.Cancel(context.Background(), runID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if result.Status != string(workflowledger.RunStatusCanceled) {
		t.Fatalf("Cancel() status = %q, want %q", result.Status, workflowledger.RunStatusCanceled)
	}

	bus.Flush()
	got := h.take()
	foundRunFinished := false
	foundCanceledStep := false
	for _, ev := range got {
		if ev.Metadata["run_id"] != runID {
			t.Fatalf("event run_id = %q, want %q (event %+v)", ev.Metadata["run_id"], runID, ev)
		}
		if ev.Kind == events.KindWorkflowRunFinished {
			foundRunFinished = true
			if ev.Detail != "canceled" {
				t.Fatalf("run_finished detail = %q, want %q", ev.Detail, "canceled")
			}
		}
		if ev.Kind == events.KindWorkflowStepCompleted && ev.Detail == "canceled" {
			foundCanceledStep = true
		}
	}
	if !foundRunFinished {
		t.Fatalf("no workflow_run_finished event after operator cancel; events = %+v", got)
	}
	if !foundCanceledStep {
		t.Fatalf("no step_completed(canceled) event after operator cancel; events = %+v", got)
	}

	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status after Cancel() = %q, want canceled", run.Status)
	}
}

// TestCoverageSessionEngineDeliverPublishesSettledRunFinished drives the
// tool-deliver tail that publishes the terminal run_finished event for a run
// the deliver path settled outside the controller. A pre-seeded succeeded
// delivery record makes delivery.Deliver replay the durable result without
// touching git, settleDeliverySuccess CASes the parked run to succeeded, and
// the engine publishes run_finished(succeeded) onto the bus (gated on the run
// having been delivery_pending before the call).
func TestCoverageSessionEngineDeliverPublishesSettledRunFinished(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeChange(t, root, runID, repo)
	key := delivery.DeliveryKey(runID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          runID,
		IdempotencyKey: key,
		Mode:           "draft",
		BaseRef:        "main",
		HeadRef:        "wf/" + run.WorktreeName,
		Provider:       "github",
		Status:         "succeeded",
		CommitSHA:      "c0ffee",
		TreeSHA:        "tree",
		DiffRef:        "diff",
		RemoteID:       "42",
		URL:            "https://github.com/x/y/pull/42",
	}); err != nil {
		t.Fatal(err)
	}

	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(root, configPath)
	e.SetEventBus(bus)

	result, err := e.Deliver(ctx, runID, true)
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if result.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("Deliver() status = %q, want %q", result.Status, workflowledger.RunStatusSucceeded)
	}
	if result.URL != "https://github.com/x/y/pull/42" {
		t.Fatalf("Deliver() URL = %q, want the replayed PR URL", result.URL)
	}

	bus.Flush()
	got := h.take()
	foundRunFinished := false
	for _, ev := range got {
		if ev.Kind == events.KindWorkflowRunFinished && ev.Detail == "succeeded" && ev.Metadata["run_id"] == runID {
			foundRunFinished = true
		}
	}
	if !foundRunFinished {
		t.Fatalf("no run_finished(succeeded) event after tool deliver; events = %+v", got)
	}

	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status after tool deliver = %q, want succeeded", fresh.Status)
	}
}

// TestCoverageSessionEngineResumeAttachesProgressBus drives the session
// engine's full resume admission path (prepareResume) with a real controller
// build: attachWorkflowProgressBus must wire the controller's progress sink
// before the controller starts, so the resumed run's lifecycle events land on
// the session event bus and the run settles to succeeded.
func TestCoverageSessionEngineResumeAttachesProgressBus(t *testing.T) {
	root, run := newForcedResumeFixture(t)
	configPath := filepath.Join(root, "config.toml")
	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(root, configPath)
	e.SetEventBus(bus)

	result, err := e.Start(context.Background(), workflowledger.StartRequest{
		Resume: true,
		RunID:  run.RunID,
	})
	if err != nil {
		t.Fatalf("Start(resume) error = %v", err)
	}
	if result.RunID != run.RunID || !result.Resumed {
		t.Fatalf("Start(resume) = %+v, want resumed run %s", result, run.RunID)
	}
	waitForSessionEngineIdle(t, e, run.RunID)

	bus.Flush()
	got := h.take()
	if len(got) == 0 {
		t.Fatal("no workflow events published for a session-engine resume: the progress bus must be attached before the controller runs")
	}
	foundRunFinished := false
	for _, ev := range got {
		if ev.Metadata["run_id"] != run.RunID {
			t.Fatalf("event run_id = %q, want %q (event %+v)", ev.Metadata["run_id"], run.RunID, ev)
		}
		if ev.Kind == events.KindWorkflowRunFinished {
			foundRunFinished = true
		}
	}
	if !foundRunFinished {
		t.Fatalf("no workflow_run_finished event for the resumed run; events = %+v", got)
	}

	repo := openWorkflowTestStore(t, filepath.Join(root, "workflow.db"))
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status after engine resume = %q, want succeeded", fresh.Status)
	}
}
