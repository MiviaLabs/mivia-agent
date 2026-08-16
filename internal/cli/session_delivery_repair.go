package cli

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowAutoDeliveryAttemptTimeout bounds one stack-drive attempt (merge-wait
// polls plus the drive loop-top check; the deliver attempt after it gets its
// own bound, workflowDeliveryTimeout). It deliberately does NOT bound the
// in-process chunk/integration controller runs (each has its own workflow
// deadline, possibly none) or the git/gh subprocess calls: aborting those at
// the session bound would kill legitimate long chunk runs, worse than holding
// the flock. A package var so tests can shorten it.
var workflowAutoDeliveryAttemptTimeout = 30 * time.Minute

// sessionAutoDeliveryRepairLoop runs one session-engine controller pass, then
// re-advances it until delivery succeeds or the run settles terminal — the
// session tool surface has no terminal operator to run "mivia workflow
// resume" the way the CLI's sync path expects (DC-9), so it re-advances
// itself. Bounded by reopenForRepair (settles delivery_failed once
// maxDeliveryRepairs is spent) and the controller's own deadline self-settle.
//
// Delivery authorization comes from the workflow's own [delivery] policy; no
// allow_publish flag is consulted.
//
// driveStack runs BEFORE delivery — delivering first would publish the plan
// PR while the chunk stack never drives. Idempotent, may be nil without a
// stacking engine; a fault or timeout stops the loop and logs, leaving the
// run delivery_pending for the reconcile sweep or 'mivia stack drive'.
//
// deliverPlanRun mirrors delivery.deliver_plan_run: when the hook drove a
// multi-chunk stack for a plan run whose own publication is disabled, the
// loop settles it succeeded without publishing (the chunk PRs carry the
// work), using a non-cancelable context since the drive hook can outlive a
// session cancel that kills runCtx.
func sessionAutoDeliveryRepairLoop(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string, advance func(context.Context) (workflowledger.RunSnapshot, error), driveStack func(context.Context) (bool, error), deliverPlanRun bool) {
	snap, err := advance(runCtx)
	if err != nil {
		settleSessionRunFailure(repo, runID, err)
		return
	}
	for {
		if snap.Status != workflowledger.RunStatusDeliveryPending {
			return
		}
		if driveStack != nil {
			// Bound one drive+deliver attempt so a stuck stack drive cannot
			// hold the execution flock forever (see the var comment). A drive
			// timeout leaves the run delivery_pending - retryable - and the
			// stack ledger is idempotent.
			driveCtx, cancelDrive := context.WithTimeout(runCtx, workflowAutoDeliveryAttemptTimeout)
			drove, err := driveStack(driveCtx)
			cancelDrive()
			if err != nil {
				log.Printf("workflow: run %s stack drive before delivery: %v", runID, err)
				return
			}
			if drove && !deliverPlanRun {
				// Plan run: the stack drove to completion and the workflow's
				// delivery policy disables publishing the plan run itself
				// (delivery.deliver_plan_run=false). The plan and its artifacts
				// are recorded in the ledger; settle the run succeeded (the same
				// terminal a delivered run reaches) and stop - nothing is
				// published for this run.
				if err := settlePlanRunSkippedDelivery(context.Background(), repo, runID); err != nil {
					log.Printf("workflow: run %s settle skipped plan run: %v", runID, err)
				}
				return
			}
		}
		// Publish authority for a stack chunk/integration run derives from
		// the stack's merge policy: this loop's own allowPublish=true below
		// authorizes a non-stacking run's (or a deliver_plan_run=true plan
		// run's) delivery, never a blanket grant (reachable-bug audit
		// finding 1; see stackRunPublishWithheld's doc comment).
		if stackRunPublishWithheld(runCtx, repo, runID, false) {
			return
		}
		if !sessionAutoDeliveryAttempt(runCtx, repo, root, res, store, runID) {
			return
		}
		snap, err = advance(runCtx)
		if err != nil {
			settleSessionRunFailure(repo, runID, err)
			return
		}
	}
}

// sessionAutoDeliveryAttempt runs one delivery attempt for runID and
// classifies the result AFTER the attempt: a successful publish settles
// succeeded; a refusal or an exhausted repair budget settles delivery_failed;
// a transient or timeout fault leaves delivery_pending (retryable later); a
// repair route returns the run to running at the named repair step. Only the
// repair route (cont=true) tells the caller to re-advance the controller and
// loop again; every other shape (including a nil deliver error from a routed
// repair - reopenForRepair prints and CASes the run, so "no error" alone must
// not be read as "published") stops the loop.
func sessionAutoDeliveryAttempt(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string) (cont bool) {
	// A FRESH delivery bound, never the caller's (possibly expired) driveCtx:
	// the delivery claim heartbeat must not die mid-publish.
	deliverCtx, cancelDeliver := context.WithTimeout(runCtx, workflowDeliveryTimeout)
	deliverErr := deliverRunWithStore(deliverCtx, root, res, store, repo, runID, true, false, io.Discard, io.Discard)
	cancelDeliver()
	// Transport faults stay unrecorded (same rule as settleDeliveryError):
	// they say nothing about the change, and a later deliver succeeds.
	if deliverErr != nil && !deliveryFaultTransient(deliverErr) {
		recordAutoDeliveryFailure(context.Background(), repo, runID, deliverErr)
	}
	fresh, getErr := repo.GetRun(context.Background(), runID)
	if getErr != nil {
		log.Printf("workflow: run %s auto-delivery repair: re-read after delivery failed: %v", runID, getErr)
		return false
	}
	switch {
	case workflowledger.IsTerminalRunStatus(fresh.Status):
		return false
	case fresh.Status == workflowledger.RunStatusDeliveryPending:
		return false
	case fresh.Status == workflowledger.RunStatusRunning && fresh.ActiveStepID != "" && !workflowledger.IsTerminalStepID(fresh.ActiveStepID):
		return true
	default:
		log.Printf("workflow: run %s auto-delivery repair: unexpected status %q; stopping loop", runID, fresh.Status)
		return false
	}
}
