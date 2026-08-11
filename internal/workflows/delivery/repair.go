package delivery

import (
	"context"
	"fmt"
	"io"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// MaxDeliveryRepairs is the DEFAULT budget for the delivery -> repair ->
// success -> delivery cycle, used when the workflow does not configure
// delivery.max_repairs. It bounds how many times a delivery rejection may
// route back into the workflow's repair step. A rejection the named repair
// step cannot actually fix would otherwise cycle until the step cap or the
// 24h run deadline is spent, and a run that repairs at the last minute is
// destroyed rather than delivered. The ceiling is higher than the original
// hard-coded 3 so a drift that needs a couple of repair iterations (for
// example a config/code mismatch like a reasoning dialect the base does not
// yet implement) can converge; the run's duration cap still bounds the total
// spend.
const MaxDeliveryRepairs = DefaultMaxDeliveryRepairs

// ReopenForRepair returns a run whose delivery failed to the step the workflow
// names in delivery.on_failure (or delivery.on_pr_metadata_failure for a
// PR-metadata defect).
//
// maxRepairs bounds this run's repair cycle; zero or a negative value selects
// MaxDeliveryRepairs. The policy snapshots delivery.max_repairs from the
// workflow TOML (see Policy.MaxRepairs).
//
// Delivery runs after the success terminal, outside the step graph. A delivery
// that fails for a reason an agent can repair - a commit hook that rejects the
// change is the common one - therefore had no route back into the workflow.
// The run stopped with all of its work done and waited for a person.
//
// The re-entry writes one attempt for the delivery and its TERMINAL failure
// outcome with a route to the repair step in ONE durable event (see
// Repository.RecordStepAttemptOutcome): the attempt is never observable in a
// non-terminal state, so a crash cannot leave a Running undeclared-step
// attempt behind. Crash windows are only before the write (nothing durable
// changed; the run returns to delivery via the success-terminal reconcile) or
// after it (the attempt is already terminal with the repair route) — both
// recoverable. The ledger derives the active step from the last attempt's
// route, so the run continues at that step on the next resume, exactly the
// way a repair loop inside the graph continues. The failure evidence is
// stored content-addressed (RepairHint, which tells the agent what to repair
// and whether a commit is involved) and referenced by the attempt, so the
// repair agent reads why delivery failed instead of guessing.
//
// The run then repairs, reaches its success terminal again, and delivers
// again. Nothing here knows what the failure was or which step repairs it:
// the workflow author names the step, so the mechanism stays generic. The
// CLI and the local engine share this one implementation.
func ReopenForRepair(ctx context.Context, repo ledger.Repository, runID, repairStep string, maxRepairs int, cause error, stdout io.Writer) error {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; list attempts for repair: %w", cause, err)
	}
	next := 1
	for _, a := range attempts {
		if a.StepID == DeliveryRepairStepID && a.AttemptNo >= next {
			next = a.AttemptNo + 1
		}
	}
	if maxRepairs <= 0 {
		maxRepairs = MaxDeliveryRepairs
	}
	if next > maxRepairs {
		// The repair budget is spent. Settle the run terminal BEFORE returning:
		// without this CAS the run stays delivery_pending forever - resume and
		// cancel both refuse that status, and cleanup removes the worktree
		// without settling, so the run looks waiting but can never be
		// delivered. delivery_failed is the honest, terminal status for a
		// delivery the named repair step could not fix. No wf-delivery attempt
		// is created here: the budget is exhausted, so this is a settle, not a
		// re-entry.
		current, err := repo.GetRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("delivery failed %d times and the repair step did not fix it: %w; read run status to settle: %w", next-1, cause, err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, current.Version, ledger.RunStatusDeliveryFailed, nil); err != nil {
			return fmt.Errorf("delivery failed %d times and the repair step did not fix it: %w; settle run to delivery_failed: %w", next-1, cause, err)
		}
		return fmt.Errorf("delivery failed %d times and the repair step did not fix it: %w", next-1, cause)
	}

	// The status CAS runs FIRST. The attempt writes below change the run's
	// derived active step, so writing them before a CAS that then fails would
	// leave a run whose active step is the repair step while its status still
	// says it is waiting for delivery - resume refuses that status, so the run
	// would be stuck in a shape no command can move.
	current, err := repo.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; read run status for repair: %w", cause, err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, current.Version, ledger.RunStatusRunning, nil); err != nil {
		return fmt.Errorf("delivery failed: %v; return run to running: %w", cause, err)
	}

	attempt := ledger.StepAttempt{
		RunID:     runID,
		AttemptID: fmt.Sprintf("wfa-%s-%d", DeliveryRepairStepID, next),
		StepID:    DeliveryRepairStepID,
		AttemptNo: next,
	}
	outcome := ledger.AttemptOutcome{
		Status:   ledger.AttemptStatusFailed,
		ErrorRef: StoreDeliveryFailureText(ctx, repo, cause),
		ToStepID: repairStep,
	}
	// The re-entry records the attempt and its terminal outcome in ONE event,
	// so a crash between a create and a complete can never leave a Running
	// undeclared-step wf-delivery attempt that resume cannot join.
	if err := repo.RecordStepAttemptOutcome(ctx, attempt, outcome); err != nil {
		return fmt.Errorf("delivery failed: %v; record delivery attempt: %w", cause, err)
	}

	fmt.Fprintf(stdout, "delivery failed: %v\n", cause)
	fmt.Fprintf(stdout, "run_id=%s status=%s repairing at step %q\n", runID, ledger.RunStatusRunning, repairStep)
	fmt.Fprintf(stdout, "continue with: mivia workflow resume %s\n", runID)
	return nil
}

// StoreDeliveryFailureText puts the harness repair hint (RepairHint) in
// content-addressed storage and returns its ref. Fail-soft: an empty ref
// costs the repair agent its evidence, but must not stop the re-entry.
func StoreDeliveryFailureText(ctx context.Context, repo ledger.Repository, cause error) string {
	if cause == nil {
		return ""
	}
	data := []byte(RepairHint(cause))
	ref := "sha256:" + ledger.DigestHex(data)
	if err := repo.StoreContent(ctx, ref, data); err != nil {
		return ""
	}
	return ref
}

// RepairHint renders the harness guidance a repair agent needs to fix a
// delivery rejection: a short "what to repair" line derived from the failure
// class, then the raw rejection text. It is project- and language-agnostic:
// it never names a repository's tests, files, tools, or gate names, so it is
// safe to ship in the binary and render for any workspace.
//
// The hint is the deterministic evidence a delivery re-entry step sees via
// delivery.failure. Without a class-specific lead the agent has to guess what
// to repair from a wall of hook output; with it, the agent is told up front
// whether the failure is a gate rejection of the change, a PR-metadata
// defect, or a permanent host refusal - and, when a commit is involved, that
// the host commits the repaired worktree before the next delivery attempt.
func RepairHint(cause error) string {
	var raw string
	if cause != nil {
		raw = cause.Error()
	}
	var lead string
	switch {
	case cause == nil:
		lead = "delivery failed without a recorded cause"
	case IsPRMetadataError(cause):
		lead = "the pull-request metadata (title/summary) was rejected; fix pr_title and pr_summary in your structured output"
	case IsRefusal(cause):
		lead = "the delivery host permanently refused publication; this is usually not fixable by a workflow step - read the reason below and correct the underlying condition"
	default:
		lead = "the delivery gate rejected the change; fix the reported failure in the worktree. If the rejection mentions uncommitted or foreign changes, make sure your repair edits are complete - the delivery host commits the worktree before the next delivery attempt (do not run git commit or push yourself)"
	}
	if raw == "" {
		return lead
	}
	return lead + "\n\nrejection output:\n" + raw
}
