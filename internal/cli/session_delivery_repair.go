package cli

import (
	"context"
	"io"
	"log"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// sessionAutoDeliveryRepairLoop runs one session-engine controller pass, then
// drives the auto-delivery repair loop that follows it.
//
// A workflow run that finishes under the session tool surface settles to
// delivery_pending and is auto-delivered in the same goroutine. When that
// delivery fails for a condition in the change (delivery.on_failure names a
// repair step), settleDeliveryError/reopenForRepair CAS the run back to
// running with the repair step routed. Before this helper, no executor
// re-advanced the controller after that: the goroutine already ran the
// controller once and exited, so the run parked at running with zero events
// (DC-9). The CLI synchronous paths print "continue with: mivia workflow
// resume"; the session tool surface has no operator at the terminal, so it
// must re-advance itself.
//
// This helper closes that gap. It re-advances the controller until delivery
// succeeds or the run settles terminal. The loop is inherently bounded:
// reopenForRepair settles delivery_failed once maxDeliveryRepairs repair
// attempts are spent, and the controller self-settles a run whose deadline
// expires, so the loop can never spin.
//
// advance runs one controller pass. Callers wire it to Controller.Run (start)
// or workflowResumeRun (resume). The first pass returns the run's settled
// snapshot; a later pass continues from the repair step.
func sessionAutoDeliveryRepairLoop(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string, allowPublish bool, advance func(context.Context) (workflowledger.RunSnapshot, error)) {
	snap, err := advance(runCtx)
	if err != nil {
		settleSessionRunFailure(repo, runID, err)
		return
	}
	for {
		if snap.Status != workflowledger.RunStatusDeliveryPending || !allowPublish {
			return
		}
		deliverErr := deliverRunWithStore(context.Background(), root, res, store, repo, runID, true, false, io.Discard, io.Discard)
		if deliverErr != nil {
			recordAutoDeliveryFailure(context.Background(), repo, runID, deliverErr)
		}
		fresh, getErr := repo.GetRun(context.Background(), runID)
		if getErr != nil {
			log.Printf("workflow: run %s auto-delivery repair: re-read after delivery failed: %v", runID, getErr)
			return
		}
		// Classify the ledger status AFTER the deliver attempt. A successful
		// publish settles succeeded; a refusal or an exhausted repair budget
		// settles delivery_failed; a transient or timeout fault leaves
		// delivery_pending (retryable later); a repair route returns the run
		// to running at the named repair step. Only the repair route
		// re-advances; every other shape stops the loop.
		//
		// The classification runs after a NIL deliver error too: a routed
		// repair makes reopenForRepair return nil (it prints and CASes the
		// run), so "no error" alone must not be read as "published".
		switch {
		case workflowledger.IsTerminalRunStatus(fresh.Status):
			return
		case fresh.Status == workflowledger.RunStatusDeliveryPending:
			return
		case fresh.Status == workflowledger.RunStatusRunning && fresh.ActiveStepID != "" && !workflowledger.IsTerminalStepID(fresh.ActiveStepID):
			snap, err = advance(runCtx)
			if err != nil {
				settleSessionRunFailure(repo, runID, err)
				return
			}
			continue
		default:
			log.Printf("workflow: run %s auto-delivery repair: unexpected status %q; stopping loop", runID, fresh.Status)
			return
		}
	}
}
