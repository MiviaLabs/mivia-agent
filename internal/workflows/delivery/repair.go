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
// names in delivery.on_failure (or the PR-metadata/diff-size variants;
// delivery.RepairTarget is the single classifier both the CLI and the local
// engine use). maxRepairs bounds the cycle; <=0 selects MaxDeliveryRepairs.
//
// Delivery runs after the success terminal, outside the step graph, so a
// repairable delivery failure (commit hook rejection is the common case)
// used to have no route back into the workflow and just waited for a person.
//
// The re-entry writes the delivery attempt and its TERMINAL failure outcome
// with a route to the repair step in ONE durable event, so the attempt is
// never observable non-terminal — a crash before the write leaves nothing
// durable changed (run returns to delivery via reconcile); a crash after
// leaves it already terminal with the repair route. Either way is
// recoverable. The ledger derives the active step from the last attempt's
// route, so the run continues at the repair step on next resume. Failure
// evidence (RepairHint: what to repair, whether a commit is involved) is
// stored content-addressed and referenced by the attempt, so the repair
// agent reads why delivery failed instead of guessing.
//
// Nothing here knows what the failure was or which step repairs it — the
// workflow author names the step, so the mechanism stays generic.
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

// RepairTarget returns the delivery repair step the policy names for a
// failure class. Diff-size rejections route to OnDiffSizeFailure (falling
// back to OnFailure), PR-metadata rejections to OnPRMetadataFailure (falling
// back to OnFailure), and every other repairable rejection to OnFailure. An
// AncestryUnverifiableError (git itself could not complete the base-ancestry
// check - a missing or corrupt object) yields an empty result unconditionally:
// no agent can repair a git object failure, so the run stays delivery_pending
// with a recorded cause and a later attempt retries. An empty result otherwise
// means the workflow declares no repair route for the class (the run holds
// for a person). It is the single classifier shared by the CLI and the local
// engine, so a delivery rejection routes to the same step on both paths.
func RepairTarget(err error, p Policy) string {
	switch {
	case IsAncestryUnverifiable(err):
		return ""
	case IsDiffSizeError(err):
		if p.OnDiffSizeFailure != "" {
			return p.OnDiffSizeFailure
		}
	case IsPRMetadataError(err):
		if p.OnPRMetadataFailure != "" {
			return p.OnPRMetadataFailure
		}
	}
	return p.OnFailure
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
// defect, an over-limit diff, or a permanent host refusal - and, when a
// commit is involved, that the host commits the repaired worktree before the
// next delivery attempt.
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
	case IsDiffSizeError(cause):
		lead = "the delivered diff is too large; the host's automatic file split either could not bring it under the hard limit or was refused because it would separate a file from its test companion (the rejection output below says which) - actually shrink the change (reduce scope, split a large edit, or move separable work out of this chunk); a file and its tests must ship in the same commit (both in the delivered commit or both deferred); keep tests green and do not commit yourself - the delivery host commits the worktree before the next delivery attempt"
	case IsRefusal(cause):
		lead = "the delivery host permanently refused publication; this is usually not fixable by a workflow step - read the reason below and correct the underlying condition"
	default:
		lead = "the delivery gate rejected the change; fix the reported failure in the worktree. If the rejection is a hook or gate failure, note that the hook verified the DELIVERED COMMIT tree while the evidence gates verified the worktree; the rejection output above lists the delivered, deferred, and worktree files when they differ. Do not revert production code to satisfy a stale test in the delivered commit - that undoes the fix; make the delivered commit internally consistent instead. If the rejection mentions uncommitted or foreign changes, make sure your repair edits are complete - the delivery host commits the worktree before the next delivery attempt (do not run git commit or push yourself)"
	}
	if raw == "" {
		return lead
	}
	return lead + "\n\nrejection output:\n" + raw
}
