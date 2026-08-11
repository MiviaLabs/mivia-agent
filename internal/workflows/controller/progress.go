package controller

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ProgressKind identifies one workflow progress event type.
type ProgressKind string

const (
	// ProgressStepStarted reports a step attempt beginning.
	ProgressStepStarted ProgressKind = "step_started"
	// ProgressStepCompleted reports a step attempt reaching a terminal status.
	ProgressStepCompleted ProgressKind = "step_completed"
	// ProgressStepHeartbeat reports a step that is still running.
	ProgressStepHeartbeat ProgressKind = "step_heartbeat"
	// ProgressGateStarted reports a gate step beginning.
	ProgressGateStarted ProgressKind = "gate_started"
	// ProgressApprovalRequested reports a run waiting for operator approval.
	ProgressApprovalRequested ProgressKind = "approval_requested"
	// ProgressRunFinished reports a run reaching a terminal status.
	ProgressRunFinished ProgressKind = "run_finished"
	// ProgressRunFailed reports a run failing.
	ProgressRunFailed ProgressKind = "run_failed"
	// ProgressPanelRefused reports a panel run refused by a member.
	ProgressPanelRefused ProgressKind = "panel_refused"
	// ProgressDeliveryStage reports one numbered delivery stage observation
	// (guard, eligibility, commit, push, pr, success, failed) with the stage
	// name and its free-form detail in Detail.
	ProgressDeliveryStage ProgressKind = "delivery_stage"
	// ProgressDeliveryRefused reports a delivery refusal: no publication
	// grant (allow_publish=false or an inactive [delivery] policy), decided
	// before any attempt.
	ProgressDeliveryRefused ProgressKind = "delivery_refused"
)

// ProgressEvent is one workflow progress observation. The CLI writer marshals
// the struct directly; no custom JSON encoding is required.
type ProgressEvent struct {
	Kind             ProgressKind
	RunID            string
	StepID           string
	TaskID           string
	CoordinatorRunID string
	AttemptNo        int
	Detail           string
	Timestamp        time.Time
}

// String renders a compact readable form of the event for logs.
func (e ProgressEvent) String() string {
	return fmt.Sprintf("%s run=%s step=%s attempt=%d detail=%s", e.Kind, e.RunID, e.StepID, e.AttemptNo, e.Detail)
}

// ProgressSink receives workflow progress events.
type ProgressSink interface {
	// Emit delivers one progress event to the consumer.
	Emit(ProgressEvent)
}

// emitProgress delivers one progress event to the configured sink. A nil sink
// makes the call a no-op. A zero timestamp and an empty run ID are completed
// from the controller clock and the controller run ID.
//
// A step-heartbeat event additionally triggers a THROTTLED durable heartbeat
// write for the attempt (the join watchdog emits one per tick; the throttle
// bounds the ledger writes). This keeps the in-memory task-id registry the
// fast liveness path while the durable event log records that the attempt was
// still running. Persistence is independent of the sink: a nil sink still
// records the durable heartbeat, and a ledger write error is best-effort
// (never fails the step).
func (c *LinearController) emitProgress(e ProgressEvent) {
	if e.Kind == ProgressStepHeartbeat && e.StepID != "" && e.AttemptNo > 0 {
		at := e.Timestamp
		if at.IsZero() {
			at = c.now()
		}
		c.persistDurableHeartbeat(attemptIDFor(e.StepID, e.AttemptNo), at)
	}
	if c.progress == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = c.now()
	}
	if e.RunID == "" {
		e.RunID = c.RunID
	}
	c.progress.Emit(e)
}

// EmitProgress publishes one progress event through the attached sink. It is
// the exported entry point for wiring an external emitter (for example the
// CoordinatorRunner step-heartbeat emitter) into this controller's sink.
func (c *LinearController) EmitProgress(e ProgressEvent) {
	c.emitProgress(e)
}

// emitStepStarted reports one step attempt beginning.
func (c *LinearController) emitStepStarted(step definition.Step, attempt workflowledger.StepAttempt) {
	c.emitProgress(ProgressEvent{
		Kind: ProgressStepStarted, StepID: step.ID, TaskID: attempt.TaskID,
		CoordinatorRunID: attempt.CoordinatorRunID, AttemptNo: attempt.AttemptNo,
	})
}

// emitStepCompleted reports one step attempt reaching its terminal status.
func (c *LinearController) emitStepCompleted(step definition.Step, attempt workflowledger.StepAttempt, status string) {
	c.emitProgress(ProgressEvent{
		Kind: ProgressStepCompleted, StepID: step.ID, TaskID: attempt.TaskID,
		CoordinatorRunID: attempt.CoordinatorRunID, AttemptNo: attempt.AttemptNo, Detail: status,
	})
}

// emitRunFinished reports the run reaching a terminal status.
func (c *LinearController) emitRunFinished(status string) {
	c.emitProgress(ProgressEvent{Kind: ProgressRunFinished, Detail: status})
}

// emitRunFailed reports the run failing with the given cause.
func (c *LinearController) emitRunFailed(detail string) {
	c.emitProgress(ProgressEvent{Kind: ProgressRunFailed, Detail: detail})
}
