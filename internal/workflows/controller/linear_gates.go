package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

func (c *LinearController) advanceEvidenceGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	if c.Verifiers == nil {
		return c.fail(ctx, run, fmt.Errorf("step %q requires a verifier catalogue", step.ID))
	}
	profile, err := c.Verifiers.Lookup(step.Verifier)
	if err != nil {
		return c.fail(ctx, run, err)
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, ok, err := c.admitAttempt(ctx, run, step.ID, attempts)
	if err != nil {
		return c.fail(ctx, run, err)
	}
	if !ok {
		return c.reconcileTerminalAttempt(ctx, run, attempt)
	}
	result, verifyErr := profile.Verify(ctx, verifier.Request{
		WorkDir: c.WorkDir,
		StepID:  step.ID,
		RunID:   c.RunID,
	})
	status := workflowledger.AttemptStatusSucceeded
	var output []byte
	var outputMap map[string]any
	if verifyErr != nil {
		status = workflowledger.AttemptStatusFailed
	} else {
		output, err = json.Marshal(result)
		if err != nil {
			return c.failAttempt(ctx, run, attempt, err)
		}
		outputMap = map[string]any{
			"status": result.Status,
			"checks": checksAsAny(result.Checks),
		}
	}
	route := RouteDecision{}
	if status == workflowledger.AttemptStatusSucceeded {
		route, err = c.selectRoute(ctx, step, status, outputMap)
		if err != nil {
			status = workflowledger.AttemptStatusFailed
			if route.ToStepID == "" {
				route = failureRoute(step)
			}
			writeCtx, cancel := stepPersistenceContext(ctx)
			defer cancel()
			_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output}, status, route)
			return c.fail(writeCtx, run, err)
		}
	} else {
		route = failureRoute(step)
	}
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	if status == workflowledger.AttemptStatusSucceeded {
		if err := c.completeSucceededRoute(writeCtx, attempt, AgentStepResult{Output: output}, route); err != nil {
			if isLoopAccountError(err) {
				return settleAfterRoute(ctx, c, run, route)
			}
			return c.fail(writeCtx, run, err)
		}
		return settleAfterRoute(ctx, c, run, route)
	}
	if err := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output}, status, route); err != nil {
		return c.fail(writeCtx, run, err)
	}
	return c.fail(writeCtx, run, verifyErr)
}

func checksAsAny(checks []verifier.Check) []any {
	out := make([]any, len(checks))
	for i, c := range checks {
		out[i] = map[string]any{"name": c.Name, "status": c.Status}
	}
	return out
}

func (c *LinearController) advanceHumanGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, found := latestAttempt(attempts, step.ID)
	if found && workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		if attempt.Status == workflowledger.AttemptStatusInterrupted {
			return c.fail(ctx, run, fmt.Errorf("human_gate step %q was interrupted", step.ID))
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

// PendingApprovalID returns the approval id for a human_gate attempt number.
func PendingApprovalID(stepID string, attemptNo int) string {
	return humanGateApprovalID(stepID, attemptNo)
}

// Approve resolves a pending human_gate and routes the step as succeeded.
// It never elevates authority, tools, delivery mode, or branch policy.
func (c *LinearController) Approve(ctx context.Context, approvalID, actor string) error {
	return c.resolveHumanGate(ctx, approvalID, actor, "approved", "")
}

// Reject resolves a pending human_gate as rejected and fails the run.
// It never elevates authority.
func (c *LinearController) Reject(ctx context.Context, approvalID, actor, reason string) error {
	return c.resolveHumanGate(ctx, approvalID, actor, "rejected", reason)
}

func (c *LinearController) resolveHumanGate(ctx context.Context, approvalID, actor, decision, reason string) error {
	if c == nil {
		return fmt.Errorf("linear controller is nil")
	}
	if strings.TrimSpace(approvalID) == "" || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("approval id and actor are required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Repo.ClaimRun(ctx, c.RunID, c.Holder); err != nil {
		return err
	}
	defer func() { _ = c.Repo.ReleaseRun(context.Background(), c.RunID, c.Holder) }()

	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		// Already finished (idempotent operator retry).
		return nil
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		return fmt.Errorf("run %q is not waiting for approval (status %q)", c.RunID, run.Status)
	}
	approvals, err := c.Repo.ListApprovals(ctx, c.RunID)
	if err != nil {
		return err
	}
	approval, found := findApproval(approvals, approvalID)
	if !found {
		return fmt.Errorf("approval %q not found", approvalID)
	}
	step, ok := c.WorkflowStep(approval.StepID)
	if !ok {
		return fmt.Errorf("approval step %q is not declared", approval.StepID)
	}
	wantStatus := "approved"
	if decision == "rejected" {
		wantStatus = "rejected"
	}
	// Idempotent resume: if already resolved to the same decision, finish the sequence.
	switch approval.Status {
	case "pending":
		if err := c.Repo.ResolveApproval(ctx, c.RunID, approvalID, actor, wantStatus, reason); err != nil {
			return err
		}
	case "approved", "rejected":
		if approval.Status != wantStatus {
			return fmt.Errorf("approval %q is already %q", approvalID, approval.Status)
		}
		// Same decision: continue incomplete complete/status writes.
	default:
		return fmt.Errorf("approval %q has unknown status %q", approvalID, approval.Status)
	}
	attemptNo, ok := attemptNoFromApprovalID(approvalID, step.ID)
	if !ok {
		return fmt.Errorf("approval %q is not a human_gate id for step %q", approvalID, step.ID)
	}
	return c.finishHumanResolutionForAttempt(ctx, run, step, decision, attemptNo)
}

func findApproval(approvals []workflowledger.ApprovalRecord, id string) (workflowledger.ApprovalRecord, bool) {
	for _, a := range approvals {
		if a.ApprovalID == id {
			return a, true
		}
	}
	return workflowledger.ApprovalRecord{}, false
}

func attemptNoFromApprovalID(approvalID, stepID string) (int, bool) {
	prefix := "wfa-approval-" + stepID + "-"
	if !strings.HasPrefix(approvalID, prefix) {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(approvalID[len(prefix):], "%d", &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func attemptByNo(attempts []workflowledger.StepAttempt, stepID string, attemptNo int) (workflowledger.StepAttempt, bool) {
	for _, a := range attempts {
		if a.StepID == stepID && a.AttemptNo == attemptNo {
			return a, true
		}
	}
	return workflowledger.StepAttempt{}, false
}

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
		if err := CompleteExistingStepResult(ctx, c.Repo, attempt, AgentStepResult{}, workflowledger.AttemptStatusFailed, route); err != nil {
			return err
		}
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
		if completeErr := CompleteExistingStepResult(ctx, c.Repo, attempt, AgentStepResult{Output: raw}, workflowledger.AttemptStatusFailed, route); completeErr != nil {
			return completeErr
		}
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
func (c *LinearController) admitAttempt(ctx context.Context, _ workflowledger.RunSnapshot, stepID string, attempts []workflowledger.StepAttempt) (workflowledger.StepAttempt, bool, error) {
	attempt, found := latestAttempt(attempts, stepID)
	if !found {
		return c.createAdmittedAttempt(ctx, stepID, nextAttemptNo(attempts, stepID), attempts)
	}
	if !workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return attempt, true, nil
	}
	reenter := attempt.Status == workflowledger.AttemptStatusInterrupted ||
		(attempt.Status == workflowledger.AttemptStatusSucceeded && attempt.ToStepID != "")
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
