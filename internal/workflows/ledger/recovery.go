package ledger

import (
	"context"
	"fmt"
)

// RecoveryPlan is the pure, ledger-typed encoding of what resuming a run
// requires. It is computed from ledger state alone — no coordinator, compiler,
// matcher or definition imports. Joining the stored coordinator run and
// re-matching evidence are caller (controller, Phase 4) responsibilities that
// consume this plan.
type RecoveryPlan struct {
	// Run is the current run snapshot.
	Run RunSnapshot
	// AttemptsInFlight are recorded attempts that never reached a terminal
	// status. Each names its stored CoordinatorRunID/TaskID: the caller must
	// JOIN those coordinator runs (query their recorded outcome) before
	// dispatching anything — a recorded attempt is never re-dispatched.
	AttemptsInFlight []StepAttempt
	// NextAttemptNo is the next fresh attempt number for the active step
	// (max recorded attempt_no for that step + 1).
	NextAttemptNo int
	// Terminal is true when the run cannot resume: it is already terminal, or
	// its derived active step is a reserved terminal step (success/failure)
	// even though the status CAS was not recorded.
	Terminal bool
	// TerminalStatus is the status the caller should record when Terminal is
	// true (succeeded for "success", failed for "failure", else the run's
	// current status).
	TerminalStatus RunStatus
	// Reason is a human-readable explanation.
	Reason string
}

// derivedActiveStep recomputes the run's derived active step from its
// recorded attempts, ordered by event sequence. The derived step is the
// transition target of the NEWEST step-bearing event: a completion's
// to_step_id, else an attempt's step_id (see Projection.ActiveStepID).
// Attempts that carry no step (empty StepID and ToStepID) contribute no
// candidate, and an empty attempt log leaves initial (the step recorded at
// admission) unchanged.
func derivedActiveStep(initial string, attempts []StepAttempt) string {
	active := initial
	for _, a := range attempts {
		if a.ToStepID != "" {
			active = a.ToStepID
		} else if a.StepID != "" {
			active = a.StepID
		}
	}
	return active
}

// PlanResume derives, purely from repository state, what resuming runID
// requires. Rules: (1) every recorded attempt with a non-terminal status is
// returned in AttemptsInFlight to be JOINED, never re-dispatched; (2) a run
// whose derived active step is a reserved terminal step is Terminal even
// without a recorded status CAS; (3) a terminal run yields Terminal; (4) a
// delivery_pending run is settled (Terminal) with TerminalStatus
// delivery_pending — never succeeded, so the resume path cannot CAS it to
// succeeded and skip delivery.
// Returns ErrNotFound if the run is absent.
func PlanResume(ctx context.Context, repo Repository, runID string) (RecoveryPlan, error) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return RecoveryPlan{}, err
	}

	plan := RecoveryPlan{Run: run, NextAttemptNo: 1}

	// Rule (3): the run's status CAS already reached a terminal status.
	if IsTerminalRunStatus(run.Status) {
		plan.Terminal = true
		plan.TerminalStatus = run.Status
		plan.Reason = fmt.Sprintf("run %s already reached terminal status %q; nothing to resume", runID, run.Status)
		return plan, nil
	}

	// Rule (4): a delivery_pending run is settled — the workflow body is
	// complete and the result is waiting for publication — but it is NOT
	// succeeded. It must never be classified as such, or the resume path
	// would CAS delivery_pending->succeeded and skip delivery. Delivery is a
	// separate host-owned step, so the plan stays delivery_pending and points
	// at `workflow deliver --allow-publish`.
	if run.Status == RunStatusDeliveryPending {
		plan.Terminal = true
		plan.TerminalStatus = RunStatusDeliveryPending
		plan.Reason = fmt.Sprintf("run %s is waiting for publication; deliver with workflow deliver --allow-publish", runID)
		return plan, nil
	}

	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return RecoveryPlan{}, err
	}

	// The derived active step is the transition target of the newest
	// step-bearing event (a completion's to_step_id, else an attempt's
	// step_id), falling back to the step recorded at admission. It is
	// recomputed here from the attempt log so the plan reflects the derived
	// step even when the repository's cached projection has not replayed it.
	activeStep := derivedActiveStep(run.ActiveStepID, attempts)
	plan.Run.ActiveStepID = activeStep

	// Rule (2): the derived active step is a reserved terminal step, so the
	// workflow is done even though the status CAS was never recorded.
	if IsTerminalStepID(activeStep) {
		plan.Terminal = true
		switch activeStep {
		case "success":
			plan.TerminalStatus = RunStatusSucceeded
		case "failure":
			plan.TerminalStatus = RunStatusFailed
		}
		plan.Reason = fmt.Sprintf("run %s routed to reserved terminal step %q; record %s", runID, activeStep, plan.TerminalStatus)
		return plan, nil
	}

	// Rule (1): recorded attempts that never reached a terminal status are
	// in flight (JOIN evidence: CoordinatorRunID/TaskID); a recorded attempt
	// is never re-dispatched. Their outcome is unresolved, so the plan
	// reports them as pending — the ledger's own record is untouched.
	// NextAttemptNo is the max attempt_no recorded for the derived active
	// step (terminal or not) plus one.
	for _, attempt := range attempts {
		if !IsTerminalAttemptStatus(attempt.Status) {
			inFlight := attempt
			inFlight.Status = AttemptStatusPending
			plan.AttemptsInFlight = append(plan.AttemptsInFlight, inFlight)
		}
		if attempt.StepID == activeStep && attempt.AttemptNo >= plan.NextAttemptNo {
			plan.NextAttemptNo = attempt.AttemptNo + 1
		}
	}
	return plan, nil
}
