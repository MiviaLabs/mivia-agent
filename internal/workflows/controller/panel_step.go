package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ErrPanelMembersComplete reports that a panel attempt completed member work
// while panelsEnabled is false, so refusePanelStep settles it failed instead
// of advancing to synthesis.
var ErrPanelMembersComplete = errors.New("panel members completed; synthesis is unavailable")

// panelsEnabled gates agent_panel execution. Wave 5 implements synthesis
// end to end (buildPanelSynthesisWork, advancePanelSynthesis,
// CompareAndSetPanelPhase), but the controller still fails panel steps
// closed: canceling a workflow mid-panel does not yet cancel its live
// member/synthesis coordinator children (D15's cancel-broker, Wave 6), and
// resume has no panel-phase reconciliation branch yet (D14, Wave 6). The
// member-running code below stays dead until Wave 6 closes those gaps. See
// plan 62's "Open question: panelsEnabled stays false".
const panelsEnabled = false

// findResumablePanelAttempt finds the step's existing non-terminal panel
// attempt, if any (D14: resume joins each existing member run from its
// exact persisted PanelExecution instead of building a new one). A fresh
// dispatch (no matching attempt) reports found=false so the caller admits
// one.
func findResumablePanelAttempt(attempts []workflowledger.StepAttempt, stepID string) (workflowledger.StepAttempt, bool) {
	for _, existing := range attempts {
		if existing.StepID == stepID && existing.PanelExecution != nil && !workflowledger.IsTerminalAttemptStatus(existing.Status) {
			return existing, true
		}
	}
	return workflowledger.StepAttempt{}, false
}

func (c *LinearController) advancePanelStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, found := findResumablePanelAttempt(attempts, step.ID)
	if !found {
		attempt, err = c.buildPanelAttempt(ctx, run, step, attempts)
		if err != nil {
			return c.fail(ctx, run, err)
		}
		if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
			return c.fail(ctx, run, err)
		}
		// Re-read the stored attempt: CreateStepAttempt records version 1 and
		// the settle CAS below compares against that version.
		stored, getErr := c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
		if getErr != nil {
			return c.fail(ctx, run, getErr)
		}
		attempt = stored
	}
	if attempt.PanelExecution.Phase == workflowledger.PanelPhaseCancelPending {
		// D14/D15: a prior cancel (via CancelRunWithAttemptsWithClaim) already
		// recorded cancel_pending durably. This branch is what makes resume
		// repair a crash between tombstone writes (cancellation matrix item
		// 10): it is unconditional, ahead of the panelsEnabled gate, because
		// reconciling an already-in-flight cancellation is cleanup, not new
		// panel dispatch, and must proceed even while panelsEnabled is false.
		return c.reconcilePanelCancelPending(ctx, run, step, attempt)
	}
	if !panelsEnabled {
		return c.refusePanelStep(ctx, run, step, attempt)
	}
	runner, ok := c.Runner.(*CoordinatorRunner)
	if !ok || runner.Coordinator == nil {
		return c.failAttempt(ctx, run, attempt, fmt.Errorf("panel step runner has no coordinator"))
	}
	panel := workflowledger.NewPanelCoordinator(c.RunID, runner.Coordinator, c.Repo)
	members := make([]PanelMemberRequest, len(attempt.PanelExecution.Members))
	for i, member := range attempt.PanelExecution.Members {
		members[i] = PanelMemberRequest{MemberID: member.MemberID, RunID: member.CoordinatorRunID}
	}
	membersResult, runErr := RunPanelMembers(ctx, c.PanelLimiter, PanelMembersRequest{AttemptID: attempt.AttemptID, Members: members, Coordinator: panel})
	if runErr != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, runErr)
	}
	return c.advancePanelSynthesis(ctx, run, step, attempt, panel, membersResult)
}

// reconcilePanelCancelPending drives one already-cancel_pending panel attempt
// toward its terminal canceled state (D14's resume-under-claim-heartbeat
// reconciliation for D15's cancel_pending phase). It is idempotent and safe
// to call on every Advance while the attempt sits in cancel_pending:
//   - ErrCancelBlocked (an ambiguous child claim) leaves the run non-terminal
//     for a later retry, without failing the run;
//   - a slow child that has not settled yet also leaves the run non-terminal;
//   - once every intended child is confirmed terminal, it completes the
//     attempt and settles the run canceled.
func (c *LinearController) reconcilePanelCancelPending(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	runner, ok := c.Runner.(*CoordinatorRunner)
	if !ok || runner.Coordinator == nil {
		return c.failAttempt(ctx, run, attempt, fmt.Errorf("panel cancel reconciliation has no coordinator"))
	}
	panel := workflowledger.NewPanelCoordinator(c.RunID, runner.Coordinator, c.Repo)
	reconciled, allTerminal, err := ReconcilePanelCancellation(ctx, c.Repo, panel, c.RunID, c.Holder, attempt.AttemptID)
	if err != nil {
		if errors.Is(err, ErrCancelBlocked) {
			return run, false, nil
		}
		return c.fail(ctx, run, err)
	}
	if !allTerminal {
		return run, false, nil
	}
	if workflowledger.IsTerminalAttemptStatus(reconciled.Status) {
		return c.settlePanelRunCanceled(ctx, run)
	}
	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusCanceled}
	outcome.ErrorRef = storeErrorText(ctx, c.Repo, errors.New("workflow run canceled by operator"))
	if err := c.Repo.CompleteStepAttempt(ctx, c.RunID, reconciled.AttemptID, reconciled.Version, outcome); err != nil {
		if errors.Is(err, workflowledger.ErrConflict) || errors.Is(err, workflowledger.ErrClaimHeld) {
			// Another executor legitimately racing the same cancel_pending
			// attempt (D14's claim-heartbeat handoff window) won this CAS
			// first, or holds the claim at the moment of the claim-fenced
			// completion write (ErrClaimHeld): both are the same retryable
			// "cannot make progress right now" outcome as ErrCancelBlocked
			// above, not a genuine defect, so it must stay non-terminal for
			// a later Advance to reconcile instead of forcing the run to a
			// durable Failed status.
			return run, false, nil
		}
		return c.fail(ctx, run, err)
	}
	return c.settlePanelRunCanceled(ctx, run)
}

// settlePanelRunCanceled CASes the run to canceled after cancel_pending
// reconciliation confirmed every intended child terminal.
func (c *LinearController) settlePanelRunCanceled(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.RunSnapshot, bool, error) {
	if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusCanceled, nil); err != nil {
		if errors.Is(err, workflowledger.ErrConflict) || errors.Is(err, workflowledger.ErrClaimHeld) {
			// Same rationale as reconcilePanelCancelPending's CompleteStepAttempt
			// conflict handling: a concurrent executor won the run-status CAS
			// first, or holds the claim at the moment of this claim-fenced
			// write (ErrClaimHeld) during the same D14 claim-heartbeat handoff
			// window, so stay non-terminal and retryable rather than escalating
			// to a permanent failure.
			return run, false, nil
		}
		return c.fail(ctx, run, err)
	}
	settled, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	return settled, true, nil
}

// refusePanelStep fails the panel attempt and its run closed with a durable
// refusal cause. The attempt is settled failed exactly like the member-failure
// path, so settleAgentAttempt persists the cause on the attempt ErrorRef. The
// ProgressPanelRefused event carries the same cause.
func (c *LinearController) refusePanelStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	cause := fmt.Sprintf("agent_panel step %q is not supported (Wave 5 synthesis unavailable)", step.ID)
	result := AgentStepResult{Status: "failed"}
	runErr := errors.New(cause)
	snapshot, done, settleErr := c.settleAgentAttempt(ctx, run, step, attempt, result, runErr)
	c.emitProgress(ProgressEvent{
		Kind: ProgressPanelRefused, StepID: step.ID, AttemptNo: attempt.AttemptNo, Detail: cause,
	})
	return snapshot, done, settleErr
}
