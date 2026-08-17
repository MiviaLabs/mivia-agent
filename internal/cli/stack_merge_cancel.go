package cli

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// cancelStackDependents transitions every queued or in-flight chunk that
// depends (directly or transitively) on a failed or canceled chunk to
// stackStatusCanceled. The closure is computed from durable task state only:
// repeatedly scan the stack task map until a full round makes no change, up to
// len(tasks) rounds. This prevents dependents from being admitted later by the
// outer drive loop after a terminal failure halted the stack.
func cancelStackDependents(ledger *tasks.Store, stackID, failedChunkID string) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	terminal := map[string]bool{
		stackStatusMerged:   true,
		stackStatusFailed:   true,
		stackStatusSkipped:  true,
		stackStatusCanceled: true,
	}
	maxRounds := len(byID)
	for round := 0; round < maxRounds; round++ {
		changed := false
		for id, t := range byID {
			if terminal[t.Status] {
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
func haltStackForFailedChunk(ledger *tasks.Store, stackID, taskID, note string) error {
	if cancelErr := cancelStackDependents(ledger, stackID, taskID); cancelErr != nil {
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
