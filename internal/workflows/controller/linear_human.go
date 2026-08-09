package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// The human-gate resolution sequence: complete the routed attempt and settle
// the run status after an approval or rejection decision. Kept separate from
// linear_gates.go so the gate admission and the resolution sequence stay
// independently readable.

func (c *LinearController) finishHumanResolution(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, decision string) error {
	return c.finishHumanResolutionForAttempt(ctx, run, step, decision, 0)
}

// finishHumanResolutionForAttempt completes the human resolution for a specific
// attempt number when attemptNo > 0. attemptNo 0 means latest attempt (legacy).
func (c *LinearController) finishHumanResolutionForAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, decision string, attemptNo int) error {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return err
	}
	var attempt workflowledger.StepAttempt
	var ok bool
	if attemptNo > 0 {
		attempt, ok = attemptByNo(attempts, step.ID, attemptNo)
	} else {
		attempt, ok = latestAttempt(attempts, step.ID)
	}
	if !ok {
		return fmt.Errorf("human_gate attempt for step %q is missing", step.ID)
	}
	// Stale approval for an older attempt must not affect a newer re-entry.
	if attemptNo > 0 {
		if latest, found := latestAttempt(attempts, step.ID); found && latest.AttemptNo > attemptNo {
			return fmt.Errorf("approval targets attempt %d but latest is %d; use the current pending approval", attemptNo, latest.AttemptNo)
		}
	}
	// Attempt already terminal: only finish run status edges for that attempt.
	if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return c.finishHumanRunStatus(ctx, run, attempt, decision)
	}
	if decision == "rejected" {
		route := failureRoute(step)
		rejectErr := fmt.Errorf("human_gate step %q was rejected", step.ID)
		if err := CompleteExistingStepResult(ctx, c.Repo, attempt, AgentStepResult{ErrorRef: storeErrorText(ctx, c.Repo, rejectErr)}, workflowledger.AttemptStatusFailed, route); err != nil {
			return err
		}
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return err
		}
		if run.Status == workflowledger.RunStatusWaitingApproval {
			return c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusFailed, nil)
		}
		return nil
	}
	output := map[string]any{"decision": "approved"}
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	route, err := c.selectRoute(ctx, step, workflowledger.AttemptStatusSucceeded, output)
	if err != nil {
		if route.ToStepID == "" {
			route = failureRoute(step)
		}
		if completeErr := CompleteExistingStepResult(ctx, c.Repo, attempt, AgentStepResult{Output: raw, ErrorRef: storeErrorText(ctx, c.Repo, err)}, workflowledger.AttemptStatusFailed, route); completeErr != nil {
			return completeErr
		}
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
		run, getErr := c.Repo.GetRun(ctx, c.RunID)
		if getErr != nil {
			return getErr
		}
		if run.Status == workflowledger.RunStatusWaitingApproval {
			_ = c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusFailed, nil)
		}
		return err
	}
	if err := c.completeSucceededRoute(ctx, attempt, AgentStepResult{Output: raw, ValidatedOutput: output}, route); err != nil {
		return err
	}
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusSucceeded))
	return c.finishHumanRunStatus(ctx, run, workflowledger.StepAttempt{ToStepID: route.ToStepID, Status: workflowledger.AttemptStatusSucceeded}, decision)
}

func (c *LinearController) finishHumanRunStatus(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, decision string) error {
	// Refresh version after prior writes.
	current, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return err
	}
	run = current
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return nil
	}
	if decision == "rejected" || attempt.Status == workflowledger.AttemptStatusFailed {
		if run.Status == workflowledger.RunStatusWaitingApproval {
			return c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusFailed, nil)
		}
		return nil
	}
	if run.Status == workflowledger.RunStatusWaitingApproval {
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return err
		}
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return err
		}
	}
	if workflowledger.IsTerminalStepID(attempt.ToStepID) {
		status := workflowledger.RunStatusSucceeded
		if attempt.ToStepID == "failure" {
			status = workflowledger.RunStatusFailed
		}
		if c.deliveryRequired() && attempt.ToStepID == "success" {
			status = workflowledger.RunStatusDeliveryPending
		}
		if workflowledger.IsTerminalRunStatus(run.Status) {
			return nil
		}
		return c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, status, nil)
	}
	return nil
}

// attemptByNo finds an attempt of stepID with the given attempt number.
func attemptByNo(attempts []workflowledger.StepAttempt, stepID string, attemptNo int) (workflowledger.StepAttempt, bool) {
	for _, a := range attempts {
		if a.StepID == stepID && a.AttemptNo == attemptNo {
			return a, true
		}
	}
	return workflowledger.StepAttempt{}, false
}
