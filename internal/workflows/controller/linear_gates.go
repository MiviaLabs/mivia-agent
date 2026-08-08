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
	profile, err := c.verifierProfile(step)
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
		WorkDir:        c.WorkDir,
		StepID:         step.ID,
		RunID:          c.RunID,
		ModuleBaseline: c.ModuleBaseline,
	})
	status := workflowledger.AttemptStatusSucceeded
	if verifyErr != nil || result.Status == "failed" {
		status = workflowledger.AttemptStatusFailed
		result.Status = "failed"
		if verifyErr == nil {
			verifyErr = fmt.Errorf("verifier %q reported failed checks", step.Verifier)
		}
	}
	// A caller deadline or cancel is a run timeout, not a verifier host failure.
	if errors.Is(verifyErr, context.DeadlineExceeded) || errors.Is(verifyErr, context.Canceled) {
		writeCtx, cancel := stepPersistenceContext(ctx)
		defer cancel()
		return c.failWithStatus(writeCtx, run, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
	}
	output, err := json.Marshal(result)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	outputMap := map[string]any{
		"status": result.Status,
		"checks": checksAsAny(result.Checks),
	}
	if status == workflowledger.AttemptStatusFailed {
		return c.routeEvidenceFailure(ctx, run, attempt, step, result, output, outputMap)
	}
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	route := RouteDecision{}
	route, err = c.selectRoute(writeCtx, step, status, outputMap)
	if err != nil {
		if route.ToStepID == "" {
			route = failureRoute(step)
		}
		_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output, ErrorRef: storeErrorText(writeCtx, c.Repo, err)}, workflowledger.AttemptStatusFailed, route)
		return c.fail(writeCtx, run, err)
	}
	if err := c.completeSucceededRoute(writeCtx, attempt, AgentStepResult{Output: output}, route); err != nil {
		if isLoopAccountError(err) {
			return settleAfterRoute(ctx, c, run, route)
		}
		return c.fail(writeCtx, run, err)
	}
	return settleAfterRoute(ctx, c, run, route)
}

// verifierProfile resolves the evidence gate's verifier: a named catalogue
// profile, or a sandboxed command profile built from the step declaration.
func (c *LinearController) verifierProfile(step definition.Step) (verifier.Profile, error) {
	if step.Command != nil {
		return verifier.NewCommandProfile(step.Command.Check, step.Command.Program, step.Command.Args, c.SecretPolicy)
	}
	if c.Verifiers == nil {
		return nil, fmt.Errorf("step %q requires a verifier catalogue", step.ID)
	}
	return c.Verifiers.Lookup(step.Verifier)
}

func (c *LinearController) routeEvidenceFailure(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, step definition.Step, result verifier.Result, output []byte, outputMap map[string]any) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	if !result.Repairable() {
		return c.settleHostFailure(writeCtx, run, attempt, step, output)
	}
	route, err := c.selectEvidenceFailureRoute(ctx, step, outputMap)
	if err != nil {
		if completeErr := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output, ErrorRef: storeErrorText(writeCtx, c.Repo, err)}, workflowledger.AttemptStatusFailed, route); completeErr != nil {
			return c.fail(writeCtx, run, completeErr)
		}
		return c.fail(writeCtx, run, err)
	}
	if err := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output}, workflowledger.AttemptStatusFailed, route); err != nil {
		return c.fail(writeCtx, run, err)
	}
	// Increment the loop counter after the repair route is durable. A failed
	// increment under-counts (one extra allowed iteration) rather than blocking
	// repair. This mirrors the crash-after-complete policy in
	// completeSucceededRoute. Without this call, checkLoopCap always reads
	// zero for evidence-gate repair routes and a finite max_iterations cap is
	// a no-op.
	if err := c.recordLoopAfterComplete(writeCtx, route); err != nil {
		// Loop counter write failed (e.g. abandon fence) but the route is
		// durable; under-count and continue rather than fail the run.
	}
	return settleAfterRoute(ctx, c, run, route)
}

func checksAsAny(checks []verifier.Check) []any {
	out := make([]any, len(checks))
	for i, c := range checks {
		item := map[string]any{"name": c.Name, "status": c.Status}
		if c.Class != "" {
			item["class"] = c.Class
		}
		if c.Detail != "" {
			item["detail"] = c.Detail
		}
		out[i] = item
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
	ctx = workflowledger.ContextWithClaimHolder(ctx, c.Holder)
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
	// The approval must belong to the run's CURRENT active gate. Replaying an
	// already-resolved approval for an earlier gate must never touch the run
	// status of a run parked at a different gate (a stale replay would flip
	// waiting_approval to running and make the current approval un-actionable).
	// A same-gate replay whose route already advanced the derived active step
	// (e.g. an approval that routed to the success terminal) is allowed: the
	// approval's step must then carry a terminal attempt routed to the run's
	// current active step.
	if approval.StepID != run.ActiveStepID {
		routed, err := c.approvalRoutedToActiveStep(ctx, approval, run.ActiveStepID)
		if err != nil {
			return err
		}
		if !routed {
			return fmt.Errorf("approval %q targets step %q, but the run is waiting on step %q", approvalID, approval.StepID, run.ActiveStepID)
		}
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

// approvalRoutedToActiveStep reports whether the approval's step already has
// a terminal attempt whose durable route leads to activeStep — the signature
// of a same-gate replay whose route advanced the derived active step.
func (c *LinearController) approvalRoutedToActiveStep(ctx context.Context, approval workflowledger.ApprovalRecord, activeStep string) (bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if attempt.StepID == approval.StepID && attempt.ToStepID == activeStep && workflowledger.IsTerminalAttemptStatus(attempt.Status) {
			return true, nil
		}
	}
	return false, nil
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
