package cli

import (
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// cancelStackDependents transitions every non-terminal chunk that depends
// (directly or transitively) on ANY failed or canceled chunk of the stack to
// stackStatusCanceled - not only on the chunk that failed in this pass: a
// chunk failed by an earlier pass blocks its dependents just the same, and
// the closure is read from durable state anyway, so naming one failed chunk
// would only narrow a correct result. The closure repeatedly scans the stack
// task map until a full round makes no change, up to len(tasks) rounds. This
// prevents dependents from being admitted later by the outer drive loop
// after a terminal failure halted the stack; stacking.StatusCanceled is
// terminal, so no reconcile pass re-admits or re-marks them.
func cancelStackDependents(ledger *workflowledger.Store, stackID string) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	maxRounds := len(byID)
	for round := 0; round < maxRounds; round++ {
		changed := false
		for id, t := range byID {
			if stackStatusIsTerminal(t.Status) {
				continue
			}
			for _, dep := range t.Deps {
				depTask, ok := byID[dep]
				if !ok {
					continue
				}
				if depTask.Status == stackStatusFailed || depTask.Status == stackStatusCanceled {
					if err := ledger.TransitionTask(stackID, id, stackStatusCanceled); err != nil {
						return fmt.Errorf("cancel dependent chunk %s: %w", id, err)
					}
					t.Status = stackStatusCanceled
					byID[id] = t
					changed = true
					break
				}
			}
		}
		if !changed {
			return nil
		}
	}
	return nil
}

// haltStackForFailedChunk cancels a terminally failed chunk's dependents and
// builds the halt error that ends the stack drive. note is the reconcile
// mark-failed note, empty on the resumed-durable-failure path.
func haltStackForFailedChunk(ledger *workflowledger.Store, stackID, taskID, note string) error {
	if cancelErr := cancelStackDependents(ledger, stackID); cancelErr != nil {
		if note == "" {
			return fmt.Errorf("stack %s halted: chunk %s failed terminally (cancel dependents: %w)", stackID, taskID, cancelErr)
		}
		return fmt.Errorf("stack %s halted: chunk %s failed terminally (%s) (cancel dependents: %w)", stackID, taskID, note, cancelErr)
	}
	if note == "" {
		return fmt.Errorf("stack %s halted: chunk %s failed terminally", stackID, taskID)
	}
	return fmt.Errorf("stack %s halted: chunk %s failed terminally (%s)", stackID, taskID, note)
}
