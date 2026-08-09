package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deliveryRequired reports whether the workflow carries an active delivery
// policy: a pull_request delivery with an explicit non-"none" mode. Runs that
// require delivery settle at delivery_pending on their success route instead
// of moving directly to succeeded.
func (c *LinearController) deliveryRequired() bool {
	return c.Workflow.DeliveryActive()
}

func (c *LinearController) reconcileTerminalRoute(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.RunSnapshot, bool, error) {
	if !workflowledger.IsTerminalStepID(run.ActiveStepID) {
		return run, false, nil
	}
	// delivery_pending is a settled pause: the run reached its terminal route
	// and waits for the delivery phase. Never CAS onward from it.
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return run, true, nil
	}
	status := workflowledger.RunStatusSucceeded
	if run.ActiveStepID == "failure" {
		status = workflowledger.RunStatusFailed
	} else if c.deliveryRequired() {
		status = workflowledger.RunStatusDeliveryPending
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		if run.Status == status {
			return run, true, nil
		}
		return run, true, fmt.Errorf("terminal route %q conflicts with run status %q", run.ActiveStepID, run.Status)
	}
	// waiting_approval cannot move directly to succeeded/failed; resume first.
	if run.Status == workflowledger.RunStatusWaitingApproval {
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return run, true, err
		}
		var err error
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return run, true, err
		}
		if run.RunID == "" {
			return run, true, fmt.Errorf("run %q not found after status transition", c.RunID)
		}
	}
	if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, status, nil); err != nil {
		return run, true, err
	}
	settled, err := c.Repo.GetRun(ctx, c.RunID)
	return settled, true, err
}

func (c *LinearController) timeoutExpiredRun(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.RunSnapshot, error) {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if settled, terminal, err := c.reconcileTerminalRoute(writeCtx, run); terminal {
		return settled, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return run, nil
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := c.Repo.CompareAndSetRunStatus(writeCtx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return run, err
		}
		var err error
		run, err = c.Repo.GetRun(writeCtx, c.RunID)
		if err != nil {
			return run, err
		}
		if settled, terminal, routeErr := c.reconcileTerminalRoute(writeCtx, run); terminal {
			return settled, routeErr
		}
	}
	// Close an open human_gate attempt so terminal runs do not leave running
	// attempts. In-flight agent attempts stay running: their runner owns the
	// join contract and reports the final state when the child exits.
	_ = c.timeoutOpenHumanAttempt(writeCtx, run)
	run, err := c.Repo.GetRun(writeCtx, c.RunID)
	if err != nil {
		return run, err
	}
	got, _, err := c.failWithStatus(writeCtx, run, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
	return got, err
}

// timeoutOpenHumanAttempt closes the open human_gate attempt as timed_out
// when the run deadline expires. In-flight agent attempts stay running: the
// runner owns the join contract and reports their final state when the child
// exits, so the controller writes no ErrorRef for them.
func (c *LinearController) timeoutOpenHumanAttempt(ctx context.Context, run workflowledger.RunSnapshot) error {
	step, ok := c.WorkflowStep(run.ActiveStepID)
	if !ok || step.Kind != "human_gate" {
		return nil
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return err
	}
	attempt, found := latestAttempt(attempts, step.ID)
	if !found || workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return nil
	}
	// Record the deadline cause on the closed attempt so the CLI can explain
	// the timeout.
	err = CompleteExistingStepResult(ctx, c.Repo, attempt, AgentStepResult{ErrorRef: storeErrorText(ctx, c.Repo, errors.New("workflow run deadline exceeded"))}, workflowledger.AttemptStatusTimedOut, RouteDecision{})
	if err != nil {
		return err
	}
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusTimedOut))
	return nil
}
