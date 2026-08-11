package controller

import (
	"context"
	"fmt"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// The human-gate approval resolution entry points and helpers. Kept separate
// from linear_gates.go so gate admission (advance/admit/pause/reconcile) and
// operator resolution (Approve/Reject) stay independently readable, matching
// the go-structure file-size policy.

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
	ctx = workflowledger.ContextWithRunID(ctx, c.RunID)
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
