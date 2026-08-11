package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func (c *LinearController) advanceHumanGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, found := latestAttempt(attempts, step.ID)
	if found && workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		if attempt.Status == workflowledger.AttemptStatusInterrupted {
			// A crashed/abandoned executor left this attempt Interrupted
			// (localengine.Interrupt's markOpenAttemptsInterrupted). That is a
			// crash artifact, not a terminal verdict: re-admit a fresh attempt
			// (No+1) and a fresh pending approval, mirroring the agent-step
			// re-entry in admitAttempt. Failing here stranded the run at a
			// terminal state with no return edge while the operator's approval
			// stayed pending (DC-1/DC-4).
			return c.admitHumanGate(ctx, run, step, attempts, attempt.AttemptNo+1)
		}
		// Re-entry after a prior successful route (repair / revisit).
		if attempt.Status == workflowledger.AttemptStatusSucceeded && attempt.ToStepID != "" {
			return c.admitHumanGate(ctx, run, step, attempts, attempt.AttemptNo+1)
		}
		return c.reconcileTerminalAttempt(ctx, run, attempt)
	}
	if !found {
		return c.admitHumanGate(ctx, run, step, attempts, nextAttemptNo(attempts, step.ID))
	}
	// Attempt exists and is still running: ensure approval + waiting status.
	return c.pauseHumanGate(ctx, run, step, attempt)
}

func (c *LinearController) admitHumanGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempts []workflowledger.StepAttempt, attemptNo int) (workflowledger.RunSnapshot, bool, error) {
	if err := c.enforceGlobalAttemptCap(attempts); err != nil {
		return c.fail(ctx, run, err)
	}
	attempt := c.newAttempt(step.ID, attemptNo)
	attempt.CoordinatorRunID = ""
	attempt.TaskID = ""
	if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
		return c.fail(ctx, run, err)
	}
	attempt, err := c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
	if err != nil {
		return c.fail(ctx, run, err)
	}
	return c.pauseHumanGate(ctx, run, step, attempt)
}

func (c *LinearController) pauseHumanGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	if err := c.ensurePendingApproval(ctx, step.ID, attempt.AttemptNo); err != nil {
		return c.fail(ctx, run, err)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusWaitingApproval, nil); err != nil {
			return run, false, err
		}
		var err error
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return run, false, err
		}
		// Report the park when the run transitions to waiting_approval.
		// Re-entry while already parked skips the CAS and stays quiet.
		c.emitProgress(ProgressEvent{
			Kind: ProgressApprovalRequested, StepID: step.ID, AttemptNo: attempt.AttemptNo,
			Detail: humanGateApprovalID(step.ID, attempt.AttemptNo),
		})
	}
	return run, true, nil
}

func (c *LinearController) ensurePendingApproval(ctx context.Context, stepID string, attemptNo int) error {
	approvalID := humanGateApprovalID(stepID, attemptNo)
	err := c.Repo.CreateApproval(ctx, workflowledger.ApprovalRecord{
		ApprovalID: approvalID,
		RunID:      c.RunID,
		StepID:     stepID,
		Status:     "pending",
	})
	if err == nil || errors.Is(err, workflowledger.ErrDuplicate) {
		return nil
	}
	return err
}

// reconcileWaitingApproval finishes partial Approve/Reject sequences and
// ensures a pending approval exists for a running human attempt.
func (c *LinearController) reconcileWaitingApproval(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.RunSnapshot, bool, error) {
	// waiting_approval cannot CAS directly to succeeded; resume through running first.
	if workflowledger.IsTerminalStepID(run.ActiveStepID) {
		if run.Status == workflowledger.RunStatusWaitingApproval {
			if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
				return run, false, err
			}
			var err error
			run, err = c.Repo.GetRun(ctx, c.RunID)
			if err != nil {
				return run, false, err
			}
		}
		return c.reconcileTerminalRoute(ctx, run)
	}
	step, ok := c.WorkflowStep(run.ActiveStepID)
	if !ok || step.Kind != "human_gate" {
		// Active step already moved past human_gate after a partial resume.
		if run.Status == workflowledger.RunStatusWaitingApproval {
			if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
				return run, false, err
			}
			run, err := c.Repo.GetRun(ctx, c.RunID)
			return run, false, err
		}
		return run, true, nil
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, found := latestAttempt(attempts, step.ID)
	if !found {
		return c.advanceHumanGate(ctx, run, step)
	}
	approvals, err := c.Repo.ListApprovals(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	approvalID := humanGateApprovalID(step.ID, attempt.AttemptNo)
	approval, hasApproval := findApproval(approvals, approvalID)
	if !hasApproval {
		// Crash after attempt create, before approval create.
		return c.pauseHumanGate(ctx, run, step, attempt)
	}
	if approval.Status == "pending" {
		if !workflowledger.IsTerminalAttemptStatus(attempt.Status) {
			return run, true, nil
		}
		if attempt.Status == workflowledger.AttemptStatusInterrupted {
			// The interrupted attempt never reached the operator (crash
			// artifact from localengine.Interrupt). Re-admit a fresh attempt
			// (No+1) and a fresh pending approval instead of failing the run
			// on a transient condition (DC-1/DC-4).
			return c.admitHumanGate(ctx, run, step, attempts, attempt.AttemptNo+1)
		}
		// Pending approval but terminal attempt is inconsistent; fail closed.
		return c.fail(ctx, run, fmt.Errorf("human_gate step %q has terminal attempt with pending approval", step.ID))
	}
	// Approval already resolved: finish incomplete complete/status path for THAT attempt.
	decision := approval.Status // approved | rejected
	attemptNo := attempt.AttemptNo
	if err := c.finishHumanResolutionForAttempt(ctx, run, step, decision, attemptNo); err != nil {
		return run, false, err
	}
	run, err = c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return run, true, nil
	}
	if run.Status == workflowledger.RunStatusWaitingApproval {
		return run, true, nil
	}
	return run, false, nil
}

// admitAttempt returns a runnable attempt for stepID. ok=false means the
// latest attempt is terminal and must be reconciled by the caller.
//
// Re-entry (repair loops): when the latest attempt already succeeded and
// recorded a route (ToStepID set), create attempt max+1 instead of treating
// the prior completion as a stuck success.
//
// A NON-terminal latest attempt is a crash artifact: only a crashed or
// force-replaced executor leaves an attempt RUNNING (the controller is
// single-threaded per run and completes each attempt before advancing). For
// agent steps, advanceAgentStep JOINS the recorded coordinator run FIRST (see
// joinInFlightAttempt) per the ledger contract — a recorded attempt is never
// re-dispatched. This branch is reached only when there is nothing to join
// (evidence gates dispatch no coordinator child, the runner has no join
// capability, or the join showed the child never ran): the stale attempt is
// marked interrupted and a fresh attempt (No+1) is admitted instead, so the
// step's work is not double-recorded under one attempt while the old
// executor's fenced writes are discarded.
func (c *LinearController) admitAttempt(ctx context.Context, _ workflowledger.RunSnapshot, stepID string, attempts []workflowledger.StepAttempt) (workflowledger.StepAttempt, bool, error) {
	attempt, found := latestAttempt(attempts, stepID)
	if !found {
		return c.createAdmittedAttempt(ctx, stepID, nextAttemptNo(attempts, stepID), attempts)
	}
	if !workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		if err := c.Repo.CompleteStepAttempt(ctx, c.RunID, attempt.AttemptID, attempt.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusInterrupted}); err != nil {
			return workflowledger.StepAttempt{}, false, err
		}
		return c.createAdmittedAttempt(ctx, stepID, attempt.AttemptNo+1, attempts)
	}
	reenter := attempt.Status == workflowledger.AttemptStatusInterrupted ||
		(attempt.Status == workflowledger.AttemptStatusSucceeded && attempt.ToStepID != "") ||
		(attempt.Status == workflowledger.AttemptStatusFailed && attempt.ToStepID != "")
	if !reenter {
		return attempt, false, nil
	}
	return c.createAdmittedAttempt(ctx, stepID, attempt.AttemptNo+1, attempts)
}

func (c *LinearController) createAdmittedAttempt(ctx context.Context, stepID string, attemptNo int, attempts []workflowledger.StepAttempt) (workflowledger.StepAttempt, bool, error) {
	if err := c.enforceGlobalAttemptCap(attempts); err != nil {
		return workflowledger.StepAttempt{}, false, err
	}
	attempt := c.newAttempt(stepID, attemptNo)
	if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
		return workflowledger.StepAttempt{}, false, err
	}
	stored, err := c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
	return stored, true, err
}

func humanGateApprovalID(stepID string, attemptNo int) string {
	return "wfa-approval-" + stepID + "-" + fmt.Sprint(attemptNo)
}
