package cliworkflow

import (
	"context"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowBusProgressSink adapts controller.ProgressSink to the session event
// bus. Each controller progress event is published as one events.Event.
type workflowBusProgressSink struct {
	bus *events.Bus
}

// Emit publishes one controller progress event onto the session event bus.
// A nil bus makes the call a no-op.
func (s workflowBusProgressSink) Emit(e controller.ProgressEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Kind:      workflowProgressKind(e.Kind),
		Timestamp: e.Timestamp,
		Name:      "workflow",
		Detail:    e.Detail,
		AgentTask: e.TaskID,
		AgentName: "workflow:" + e.StepID,
		Metadata: map[string]string{
			"run_id":             e.RunID,
			"step":               e.StepID,
			"attempt":            strconv.Itoa(e.AttemptNo),
			"coordinator_run_id": e.CoordinatorRunID,
			"task_id":            e.TaskID,
		},
	})
}

// workflowProgressKind maps one controller progress kind onto the session
// event kind. A run failure and a panel refusal reuse the finished and
// completed kinds with the cause in Detail.
func workflowProgressKind(k controller.ProgressKind) events.Kind {
	switch k {
	case controller.ProgressRunStarted:
		return events.KindWorkflowRunStarted
	case controller.ProgressStepStarted:
		return events.KindWorkflowStepStarted
	case controller.ProgressStepCompleted:
		return events.KindWorkflowStepCompleted
	case controller.ProgressStepHeartbeat:
		return events.KindWorkflowStepHeartbeat
	case controller.ProgressGateStarted:
		return events.KindWorkflowGateResult
	case controller.ProgressApprovalRequested:
		return events.KindWorkflowApprovalRequested
	case controller.ProgressRunFinished:
		return events.KindWorkflowRunFinished
	case controller.ProgressRunFailed:
		return events.KindWorkflowRunFinished
	case controller.ProgressPanelRefused:
		return events.KindWorkflowStepCompleted
	case controller.ProgressPanelMemberFailed:
		// A panel member that failed under allow_partial is a state change
		// the operator may need to act on: the panel synthesizes from fewer
		// reviewers than the workflow declares, and the step still settles
		// succeeded. Reusing the completed kind with the cause in Detail is
		// the ProgressPanelRefused precedent. Falling through to the default
		// below sent it as a heartbeat, which the TUI notice bridge silences
		// by design - so a degraded review reached the operator as nothing.
		return events.KindWorkflowStepCompleted
	case controller.ProgressDeliveryStage, controller.ProgressDeliveryRefused, controller.ProgressChunkScopeDropped:
		return events.KindWorkflowDeliveryStage
	default:
		return events.KindWorkflowStepHeartbeat
	}
}

// workflowProgressSink resolves the engine's event bus provider into a
// progress sink. The provider is read here, so a bus created after wiring is
// still observed. A nil provider or a provider returning nil disables
// progress publishing.
func (e *sessionWorkflowEngine) workflowProgressSink() controller.ProgressSink {
	e.mu.Lock()
	provider := e.bus
	e.mu.Unlock()
	if provider == nil {
		return nil
	}
	bus := provider()
	if bus == nil {
		return nil
	}
	return workflowBusProgressSink{bus: bus}
}

// publishDeliveredRunFinished publishes one run_finished(succeeded) event for
// a run the session auto-delivery path settled. Delivery completes outside the
// controller (which parked at delivery_pending and emitted no run_finished),
// so the terminal event is published here. The delivery record keys the
// emission: only a delivered run carries one, so a controller-finished run
// (which already emitted run_finished) is never double-published.
func (e *sessionWorkflowEngine) publishDeliveredRunFinished(ctx context.Context, repo workflowledger.Repository, runID string) {
	sink := e.workflowProgressSink()
	if sink == nil {
		return
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil || run.Status != workflowledger.RunStatusSucceeded {
		return
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil || (rec.Status != "succeeded" && rec.Status != "no_diff") {
		return
	}
	sink.Emit(controller.ProgressEvent{
		Kind: controller.ProgressRunFinished, RunID: runID, Detail: "succeeded",
		Timestamp: time.Now(),
	})
}

// publishSkippedPlanRunFinished publishes one run_finished(succeeded) event for
// a plan run the session auto-delivery path settled WITHOUT publishing it: the
// workflow's delivery.deliver_plan_run option is false, so after the stack
// drove, the loop CASed the plan run to succeeded and no delivery record was
// written. publishDeliveredRunFinished keys on the delivery record, so it
// cannot emit for this shape; the completed stack drive - the succeeded
// decompose output plus every chunk task merged, the same durable state the
// driver checks (StackDriveCompleted) - is the marker that this run completed
// via the skip path. A seeded-but-incomplete stack (the drive aborted after
// seeding) never emits run_finished. A controller-finished succeeded run -
// which already emitted run_finished - has no completed stack drive, so it is
// never double-published.
//
// Mutual exclusion with publishDeliveredRunFinished: LaunchStartedWorkflow and
// launchResume run the two publishers back to back, and a delivered plan run
// carries BOTH a succeeded delivery record and a seeded stack plan. This
// publisher therefore fires only for succeeded runs WITHOUT a delivery record
// (the skip-settled shape); any delivery record for the run means
// publishDeliveredRunFinished owns the terminal event.
func (e *sessionWorkflowEngine) publishSkippedPlanRunFinished(ctx context.Context, store *storage.SQLite, repo workflowledger.Repository, runID string) {
	sink := e.workflowProgressSink()
	if sink == nil || store == nil {
		return
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil || run.Status != workflowledger.RunStatusSucceeded {
		return
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(runID, run.WorkflowDigest)); err == nil {
		return // delivered runs publish via publishDeliveredRunFinished
	}
	// Display verdict only (a run_finished TUI event for an already-settled
	// run): the settle paths keep the merge oracle, and an event emitter must
	// not run network probes, so the durable pushed evidence decides.
	if !StackDriveCompleted(ctx, e.root, store, repo, runID, StackPlanMergePolicy(ctx, repo, runID), false) {
		return
	}
	sink.Emit(controller.ProgressEvent{
		Kind: controller.ProgressRunFinished, RunID: runID, Detail: "succeeded",
		Timestamp: time.Now(),
	})
}

// publishCanceledAttempts emits one step_completed(canceled) progress event
// per attempt an operator cancel settled, via the engine event bus adapter.
func (e *sessionWorkflowEngine) publishCanceledAttempts(runID string, attempts []workflowledger.StepAttempt) {
	sink := e.workflowProgressSink()
	if sink == nil {
		return
	}
	for _, attempt := range attempts {
		sink.Emit(controller.ProgressEvent{
			Kind:             controller.ProgressStepCompleted,
			RunID:            runID,
			StepID:           attempt.StepID,
			AttemptNo:        attempt.AttemptNo,
			TaskID:           attempt.TaskID,
			CoordinatorRunID: attempt.CoordinatorRunID,
			Detail:           "canceled",
			Timestamp:        time.Now(),
		})
	}
}
