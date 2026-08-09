package delivery

import (
	"context"
	"fmt"
	"io"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// MaxDeliveryRepairs bounds the delivery -> repair -> success -> delivery
// cycle. A rejection the named repair step cannot actually fix would otherwise
// cycle until the step cap or the 24h run deadline is spent, and a run that
// repairs at the last minute is destroyed rather than delivered.
const MaxDeliveryRepairs = 3

// ReopenForRepair returns a run whose delivery failed to the step the workflow
// names in delivery.on_failure (or delivery.on_pr_metadata_failure for a
// PR-metadata defect).
//
// Delivery runs after the success terminal, outside the step graph. A delivery
// that fails for a reason an agent can repair - a commit hook that rejects the
// change is the common one - therefore had no route back into the workflow.
// The run stopped with all of its work done and waited for a person.
//
// The re-entry writes one attempt for the delivery and completes it as failed
// with a route to the repair step. The ledger derives the active step from the
// last attempt's route, so the run continues at that step on the next resume,
// exactly the way a repair loop inside the graph continues. The failure text
// is stored content-addressed and referenced by the attempt, so the repair
// agent reads why delivery failed instead of guessing.
//
// The run then repairs, reaches its success terminal again, and delivers
// again. Nothing here knows what the failure was or which step repairs it:
// the workflow author names the step, so the mechanism stays generic. The
// CLI and the local engine share this one implementation.
func ReopenForRepair(ctx context.Context, repo ledger.Repository, runID, repairStep string, cause error, stdout io.Writer) error {
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
	if next > MaxDeliveryRepairs {
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
		Status:    ledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return fmt.Errorf("delivery failed: %v; record delivery attempt: %w", cause, err)
	}

	fresh, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; read delivery attempt: %w", cause, err)
	}
	outcome := ledger.AttemptOutcome{
		Status:   ledger.AttemptStatusFailed,
		ErrorRef: StoreDeliveryFailureText(ctx, repo, cause),
		ToStepID: repairStep,
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, fresh.Version, outcome); err != nil {
		return fmt.Errorf("delivery failed: %v; route delivery attempt to %q: %w", cause, repairStep, err)
	}

	fmt.Fprintf(stdout, "delivery failed: %v\n", cause)
	fmt.Fprintf(stdout, "run_id=%s status=%s repairing at step %q\n", runID, ledger.RunStatusRunning, repairStep)
	fmt.Fprintf(stdout, "continue with: mivia workflow resume %s\n", runID)
	return nil
}

// StoreDeliveryFailureText puts the failure text in content-addressed storage
// and returns its ref. Fail-soft: an empty ref costs the repair agent its
// evidence, but must not stop the re-entry.
func StoreDeliveryFailureText(ctx context.Context, repo ledger.Repository, cause error) string {
	if cause == nil {
		return ""
	}
	data := []byte(cause.Error())
	ref := "sha256:" + ledger.DigestHex(data)
	if err := repo.StoreContent(ctx, ref, data); err != nil {
		return ""
	}
	return ref
}
