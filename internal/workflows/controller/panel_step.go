package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ErrPanelMembersComplete reports that a panel attempt completed member work
// while panelsEnabled is false, so refusePanelStep settles it failed instead
// of advancing to synthesis.
var ErrPanelMembersComplete = errors.New("panel members completed; synthesis is unavailable")

// ErrCancelReconciliationPending reports that reconcilePanelCancelPending
// made no terminal progress this Advance (an ambiguous child claim, a slow
// child, or a claim conflict from a racing executor) and wants another
// Advance later. It is distinct from ErrPanelMembersComplete: without this
// sentinel, Run's loop cannot tell a not-yet-terminal cancel_pending
// reconciliation apart from the "members complete, synthesis unsupported"
// case in refusePanelStep, since both leave Advance returning done=false,
// err=nil with the active step still agent_panel. Conflating the two would
// make Run treat every such case as ErrPanelMembersComplete, which
// isNonTerminalWorkflowStop settles as a silent no-op - stranding a
// legitimately still-canceling run at running/cancel_pending with no
// automatic retry. Callers should retry Advance/Run on this error (see
// RunWithCancelReconciliationRetry) instead of treating it as a stop.
var ErrCancelReconciliationPending = errors.New("panel cancel reconciliation is not yet complete")

// panelsEnabled gates agent_panel execution. Dispatch (buildPanelAttempt,
// RunPanelMembers), synthesis (buildPanelSynthesisWork, advancePanelSynthesis,
// CompareAndSetPanelPhase), cancel-broker reconciliation (D15,
// reconcilePanelCancelPending), and resume rejoining (D14,
// findResumablePanelAttempt) are all implemented and covered by a hostile
// concurrency audit and the full verification gate (build, vet, structure
// checks, race, secret scan, docs, semgrep). See plan 62's completion record
// for the audit history and any still-open follow-ups.
const panelsEnabled = true

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

// panelMemberFailureDetail renders one failed member as "<memberID>: <cause>"
// for progress/error reporting. A member can fail with nil Err when the
// coordinator returns no Result, so it falls back to a stable placeholder.
func panelMemberFailureDetail(r PanelMemberResult) string {
	if r.Err != nil {
		return fmt.Sprintf("%s: %v", r.MemberID, r.Err)
	}
	return fmt.Sprintf("%s: no coordinator result", r.MemberID)
}

// panelMemberFailureError builds the settle error describing the first failed
// member. It never wraps a nil error.
func panelMemberFailureError(r PanelMemberResult) error {
	return errors.New(panelMemberFailureDetail(r))
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
	// The durable panel phase is the authority on which children may still
	// run. members_admitted means the members still need dispatch (and
	// synthesis is not admitted yet). synthesis_admitted is the crash window
	// where CompareAndSetPanelPhase committed members_admitted ->
	// synthesis_admitted but the executor died before EnsureSynthesis ran:
	// resume must JOIN the already-persisted synthesis child (D14), never
	// re-run members. Re-dispatching members for a synthesis_admitted attempt
	// would trip PanelCoordinator's requireRunnablePhase(members_admitted)
	// ErrConflict on every member (panel_runner.go's MemberNeedsActorPermit /
	// panel_coordinator.go's EnsureMember) and settle the run failed for no
	// reason. Any other phase fails closed with a clear cause.
	switch attempt.PanelExecution.Phase {
	case workflowledger.PanelPhaseMembersAdmitted:
		return c.advancePanelMembersAdmitted(ctx, run, step, attempt, panel)
	case workflowledger.PanelPhaseSynthesisAdmitted:
		// The members completed before the crash and their bounded reports
		// are persisted inside the synthesis work input; advancePanelSynthesis
		// rebuilds the envelope from that persisted input on this branch, so
		// no in-memory member results are needed and none are re-dispatched.
		return c.advancePanelSynthesis(ctx, run, step, attempt, panel, PanelMembersResult{})
	default:
		return c.failAttempt(ctx, run, attempt, fmt.Errorf("panel step %q has unexpected phase %q", step.ID, attempt.PanelExecution.Phase))
	}
}

// advancePanelMembersAdmitted runs the admitted panel members and applies the
// step's failure policy to their outcomes. RunPanelMembers is policy-agnostic:
// it returns every member outcome (success and failure alike) and only
// surfaces request-level errors. require_all preserves the legacy behavior
// (any member failure settles the attempt failed); allow_partial proceeds to
// synthesis as long as at least one member succeeded, reporting each failed
// member via ProgressPanelMemberFailed, and settles failed only when ALL
// members fail.
func (c *LinearController) advancePanelMembersAdmitted(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, panel workflowledger.PanelCoordinator) (workflowledger.RunSnapshot, bool, error) {
	members := make([]PanelMemberRequest, len(attempt.PanelExecution.Members))
	for i, member := range attempt.PanelExecution.Members {
		members[i] = PanelMemberRequest{MemberID: member.MemberID, RunID: member.CoordinatorRunID}
	}
	membersResult, runErr := RunPanelMembers(ctx, c.PanelLimiter, PanelMembersRequest{AttemptID: attempt.AttemptID, Members: members, Coordinator: panel})
	if runErr != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, runErr)
	}
	policy := ""
	if step.Panel != nil {
		policy = step.Panel.FailurePolicy
	}
	var failedMembers []PanelMemberResult
	for _, r := range membersResult.Members {
		if r.Err != nil || r.Result == nil {
			failedMembers = append(failedMembers, r)
		}
	}
	switch {
	case len(failedMembers) > 0 && policy == definition.PanelFailurePolicyRequireAll:
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, panelMemberFailureError(failedMembers[0]))
	case len(failedMembers) == len(membersResult.Members) && policy == definition.PanelFailurePolicyAllowPartial:
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, panelMemberFailureError(failedMembers[0]))
	case len(failedMembers) > 0 && policy == definition.PanelFailurePolicyAllowPartial:
		// Some members succeeded: report every failed member and continue.
		for _, r := range failedMembers {
			c.emitProgress(ProgressEvent{
				Kind: ProgressPanelMemberFailed, StepID: step.ID, AttemptNo: attempt.AttemptNo,
				Detail: panelMemberFailureDetail(r),
			})
		}
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
			return run, false, ErrCancelReconciliationPending
		}
		return c.fail(ctx, run, err)
	}
	if !allTerminal {
		return run, false, ErrCancelReconciliationPending
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
			return run, false, ErrCancelReconciliationPending
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
			return run, false, ErrCancelReconciliationPending
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

// cancelReconciliationRetryLimit bounds automatic retries of a not-yet-
// terminal panel cancel reconciliation before RunWithCancelReconciliationRetry
// gives up. It never busy-loops or retries forever on a genuinely stuck
// claim: a later operator cancel or resume can still reconcile it.
const cancelReconciliationRetryLimit = 5

// cancelReconciliationRetryDelay bounds each retry's backoff.
const cancelReconciliationRetryDelay = 500 * time.Millisecond

// RunWithCancelReconciliationRetry runs run repeatedly while it reports
// ErrCancelReconciliationPending, so a panel cancel_pending attempt that
// is not yet all-terminal (a slow-to-stop member, an ambiguous claim, or a
// racing executor's claim conflict) gets automatically retried instead of
// stranding the run at running with no driver ever calling Advance again.
// It gives up after cancelReconciliationRetryLimit retries or ctx
// cancellation, returning whatever run last reported.
func RunWithCancelReconciliationRetry(ctx context.Context, run func(context.Context) (workflowledger.RunSnapshot, error)) (workflowledger.RunSnapshot, error) {
	var snap workflowledger.RunSnapshot
	var err error
	for attempt := 0; ; attempt++ {
		snap, err = run(ctx)
		if !errors.Is(err, ErrCancelReconciliationPending) || attempt >= cancelReconciliationRetryLimit {
			return snap, err
		}
		select {
		case <-ctx.Done():
			return snap, err
		case <-time.After(cancelReconciliationRetryDelay):
		}
	}
}
