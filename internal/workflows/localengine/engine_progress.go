package localengine

// engine_progress.go: the package progress sink for localengine terminal
// operations. Cancel and delivery-completion settle outside a controller (the
// only other progress source), so those paths publish through this hook.
// Hosts wire it once at startup with SetProgressSink, typically with a bus
// adapter such as NewBusProgressSink; nil disables publishing.

import (
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// progressSink is the package progress sink; nil disables publishing.
var progressSink controller.ProgressSink

// SetProgressSink wires the package progress sink. Call it once at startup,
// before any run, from a single goroutine. A nil sink disables publishing.
func SetProgressSink(s controller.ProgressSink) {
	progressSink = s
}

// NewBusProgressSink adapts a controller progress sink to an events.Bus: each
// terminal progress event is published as one events.Event with the workflow
// kind mapping and run/step attribution, mirroring the session engine's
// workflowBusProgressSink adapter.
func NewBusProgressSink(bus *events.Bus) controller.ProgressSink {
	return busProgressSink{bus: bus}
}

// busProgressSink publishes controller progress events onto an events.Bus.
type busProgressSink struct {
	bus *events.Bus
}

// Emit publishes one controller progress event onto the bus.
func (s busProgressSink) Emit(e controller.ProgressEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Kind:      localProgressKind(e.Kind),
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

// localProgressKind maps one localengine terminal progress kind onto the
// session event kind. Delivery stage and refusal observations reuse the
// workflow_delivery_stage event kind with the cause in Detail. Unrecognised
// kinds fall back to a heartbeat tick.
func localProgressKind(k controller.ProgressKind) events.Kind {
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
		return events.KindWorkflowStepCompleted
	case controller.ProgressDeliveryStage, controller.ProgressDeliveryRefused, controller.ProgressChunkScopeDropped:
		return events.KindWorkflowDeliveryStage
	default:
		return events.KindWorkflowStepHeartbeat
	}
}

// emitProgress delivers one terminal progress event to the package progress
// sink. A nil sink makes the call a no-op.
func emitProgress(e controller.ProgressEvent) {
	if progressSink == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	progressSink.Emit(e)
}

// emitDeliveredRunFinished publishes run_finished(succeeded) after delivery
// CASes the run to succeeded.
func emitDeliveredRunFinished(runID string) {
	emitProgress(controller.ProgressEvent{
		Kind: controller.ProgressRunFinished, RunID: runID, Detail: "succeeded",
	})
}

// deliveryStageEmitter returns the delivery Stage callback for one delivery
// attempt: each numbered delivery stage is published through the package
// progress sink as one workflow_delivery_stage event (a nil sink no-ops).
// The stable stage name and its free-form detail ride together in Detail
// ("push: push branch wf/x to origin"), attributed to the run and the
// synthetic "deliver" step.
func (e *Engine) deliveryStageEmitter(runID string) func(stage, detail string) {
	return func(stage, detail string) {
		emitProgress(controller.ProgressEvent{
			Kind:      controller.ProgressDeliveryStage,
			RunID:     runID,
			StepID:    "deliver",
			Detail:    stage + ": " + detail,
			Timestamp: time.Now(),
		})
	}
}

// emitDeliveryRefused publishes one workflow_delivery_stage event carrying a
// delivery refusal (no publication grant, or a pre-attempt refusal): the
// refusal reason rides in Detail.
func (e *Engine) emitDeliveryRefused(runID, reason string) {
	emitProgress(controller.ProgressEvent{
		Kind:      controller.ProgressDeliveryRefused,
		RunID:     runID,
		StepID:    "deliver",
		Detail:    reason,
		Timestamp: time.Now(),
	})
}

// emitCanceledAttempts publishes one step_completed(canceled) per attempt an
// operator cancel settled.
func emitCanceledAttempts(runID string, attempts []workflowledger.StepAttempt) {
	for _, attempt := range attempts {
		emitProgress(controller.ProgressEvent{
			Kind:             controller.ProgressStepCompleted,
			RunID:            runID,
			StepID:           attempt.StepID,
			AttemptNo:        attempt.AttemptNo,
			TaskID:           attempt.TaskID,
			CoordinatorRunID: attempt.CoordinatorRunID,
			Detail:           "canceled",
		})
	}
}
