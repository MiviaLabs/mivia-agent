package cliorchestrate

import (
	"context"
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// salvageReadTimeout bounds the ledger read that recovers an unjoined run. The
// caller's context is already expired, so this deliberately uses a fresh one.
const salvageReadTimeout = 5 * time.Second

// salvageUnjoinedRun rebuilds a RunResult from the ledger for a run whose Join was
// cut short by the caller's own context, carrying the Join error so the failure is
// still reported. Returns nil when there is nothing to salvage, leaving the caller
// to report the bare error.
//
// The run itself is not cancelled: its pool context is rooted in Background, so it
// keeps going and stays reachable through inspect_agents and join_run on the handle
// registered above. Cancelling here would destroy in-flight work to tidy up a
// budget the caller had already spent.
func salvageUnjoinedRun(c coordinator.Coordinator, handle *coordinator.RunHandle, joinErr error) *coordinator.RunResult {
	if !errors.Is(joinErr, context.DeadlineExceeded) && !errors.Is(joinErr, context.Canceled) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), salvageReadTimeout)
	defer cancel()
	snap, err := c.Inspect(ctx, handle)
	if err != nil || !hasSalvageableWork(snap.Tasks) {
		return nil
	}
	return &coordinator.RunResult{
		Snapshot: snap,
		Results:  coordinator.ResultsFromSnapshots(snap.Tasks),
		Err:      joinErr,
	}
}

// hasSalvageableWork reports whether any task reached an outcome worth preserving.
//
// A run cut off before anything ran has nothing to lose, and salvaging it would
// hand the caller a payload of "queued" tasks that reads as "nothing went wrong" -
// strictly worse than the plain error, because dispatch_tasks' per-task array has
// no run-level field to carry the cancellation. Only salvage when there is real
// work the error would otherwise discard.
func hasSalvageableWork(tasks []ledger.TaskSnapshot) bool {
	for _, task := range tasks {
		switch ledger.TaskStatus(task.Status) {
		case ledger.TaskStatusCompleted, ledger.TaskStatusFailed,
			ledger.TaskStatusTimedOut, ledger.TaskStatusCanceled,
			ledger.TaskStatusBlocked:
			return true
		}
	}
	return false
}
