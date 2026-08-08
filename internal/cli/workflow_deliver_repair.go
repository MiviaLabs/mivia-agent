package cli

import (
	"context"
	"fmt"
	"io"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deliveryRepairStepID is the step ID under which a failed delivery is
// recorded. Delivery is not a step in the workflow graph, but the ledger
// tracks the active step through attempts, so the re-entry needs an attempt to
// carry the route. The ID is reserved: a workflow cannot declare a step with
// this name, because step IDs come from the definition and this one does not.
const deliveryRepairStepID = "delivery"

// reopenForRepair returns a run whose delivery failed to the step the workflow
// names in delivery.on_failure.
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
// the workflow author names the step, so the mechanism stays generic.
func reopenForRepair(ctx context.Context, repo workflowledger.Repository, runID, repairStep string, cause error, stdout io.Writer) error {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; list attempts for repair: %w", cause, err)
	}
	next := 1
	for _, a := range attempts {
		if a.StepID == deliveryRepairStepID && a.AttemptNo >= next {
			next = a.AttemptNo + 1
		}
	}

	attempt := workflowledger.StepAttempt{
		RunID:     runID,
		AttemptID: fmt.Sprintf("wfa-%s-%d", deliveryRepairStepID, next),
		StepID:    deliveryRepairStepID,
		AttemptNo: next,
		Status:    workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return fmt.Errorf("delivery failed: %v; record delivery attempt: %w", cause, err)
	}

	fresh, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; read delivery attempt: %w", cause, err)
	}
	outcome := workflowledger.AttemptOutcome{
		Status:   workflowledger.AttemptStatusFailed,
		ErrorRef: storeDeliveryFailureText(ctx, repo, cause),
		ToStepID: repairStep,
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, fresh.Version, outcome); err != nil {
		return fmt.Errorf("delivery failed: %v; route delivery attempt to %q: %w", cause, repairStep, err)
	}

	current, err := repo.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("delivery failed: %v; read run status for repair: %w", cause, err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, current.Version, workflowledger.RunStatusRunning, nil); err != nil {
		return fmt.Errorf("delivery failed: %v; return run to running: %w", cause, err)
	}

	fmt.Fprintf(stdout, "delivery failed: %v\n", cause)
	fmt.Fprintf(stdout, "run_id=%s status=%s repairing at step %q\n", runID, workflowledger.RunStatusRunning, repairStep)
	fmt.Fprintf(stdout, "continue with: mivia workflow resume %s\n", runID)
	return nil
}

// storeDeliveryFailureText puts the failure text in content-addressed storage
// and returns its ref. Fail-soft: an empty ref costs the repair agent its
// evidence, but must not stop the re-entry.
func storeDeliveryFailureText(ctx context.Context, repo workflowledger.Repository, cause error) string {
	if cause == nil {
		return ""
	}
	data := []byte(cause.Error())
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := repo.StoreContent(ctx, ref, data); err != nil {
		return ""
	}
	return ref
}
