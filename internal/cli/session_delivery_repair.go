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
// Delivery authorization comes from the workflow itself: a run settles at
// delivery_pending only when its workflow declares a [delivery] policy, and
// that policy is the publication grant. No allow_publish flag is consulted -
// the harness must publish a delivery-defined workflow always, without flags
// or manual overrides. deliverRunWithStore independently refuses a run whose
// workflow has no active delivery policy.
//
// advance runs one controller pass. Callers wire it to Controller.Run (start)
// or workflowResumeRun (resume). The first pass returns the run's settled
// snapshot; a later pass continues from the repair step.
//
// driveStack is the stacking hook: for a run that settles at delivery_pending
// with a multi-chunk plan it drives the chunk stack to completion and reports
// whether it drove one. It runs BEFORE delivery - the plan run's success
// terminal is delivery-policy active, so it parks at delivery_pending, and
// delivering first would publish the plan PR while the chunk stack never
// drives (deliver-before-drive ordering bug). The hook is the whole stack
// drive (chunks -> merges -> integration run) and is idempotent, so
// re-invocation on a later repair cycle is safe. It may be nil for callers
// without a stacking engine. A drive fault is logged and the loop stops: the
// run stays delivery_pending (never falsely published) and the seeded stack
// ledger remains resumable via `mivia stack drive`.
//
// deliverPlanRun is the resolved delivery.deliver_plan_run option (default
// false). When the hook drove a multi-chunk stack for a plan run whose own
// publication is disabled, the loop settles the plan run succeeded and stops
// without publishing anything for it: the chunk PRs carry the work, and the
// plan and its artifacts stay recorded in the ledger. The settle uses a
// non-cancelable context deliberately (mirroring the CLI skip path in
// executeWorkflowRun): the drive hook is uninterruptible and can outlive a
// session cancel that kills runCtx, so the terminal CAS must survive it or
// the run strands at delivery_pending with the stack complete.
func sessionAutoDeliveryRepairLoop(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string, advance func(context.Context) (workflowledger.RunSnapshot, error), driveStack func() (bool, error), deliverPlanRun bool) {
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
			drove, err := driveStack()
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
