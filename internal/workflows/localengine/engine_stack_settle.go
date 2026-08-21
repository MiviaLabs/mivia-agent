package localengine

// engine_stack_settle.go: the completion half of the engine's stack drive
// (drive-before-delivery). Once every chunk merged, finishStack admits the
// final full-suite integration run, waits for it to settle (delivering it
// under the auto merge policy, and under auto also waiting for its PR to
// merge), then settles the plan run's own delivery_pending terminal. The
// status/transition helpers and the drive-completed verification that the
// delivery gate relies on live here too.

import (
	"context"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// finishStack admits the final full-suite integration run once every chunk is
// merged, waits for it to settle (delivering it under the auto merge policy,
// and under auto also waiting for its PR to merge), then settles the plan
// run: deliver_plan_run=false settles the plan run succeeded without
// publishing (the chunk PRs carry the reviewable work); deliver_plan_run=true
// leaves it delivery_pending for an explicit workflow_deliver (mirrors the
// CLI's settleStackPlanRunIfComplete).
func (e *Engine) finishStack(ctx context.Context, planRun workflowledger.RunSnapshot, compiled *definition.CompiledWorkflow, ledger *tasks.Store, stackID string, planInputs map[string]string, prBase string) {
	autoPublish := compiled.Stacking.MergePolicy == "auto"
	inputs, _ := stacking.IntegrationRunInputs(planInputs, prBase)
	key, err := stacking.AdmissionKey(stackID, stacking.IntegrationChunkID)
	if err != nil {
		log.Printf("workflow: drive stack %s: integration key: %v", stackID, err)
		return
	}
	run, found, err := e.stackRunByKey(ctx, stackID, stacking.IntegrationChunkID)
	if err != nil {
		log.Printf("workflow: drive stack %s: integration lookup: %v", stackID, err)
		return
	}
	if !found {
		res, serr := e.Start(ctx, agenttools.StartRequest{
			Workflow:      planRun.WorkflowName,
			Inputs:        inputs,
			InvocationKey: key,
			AllowPublish:  autoPublish,
		})
		if serr != nil {
			log.Printf("workflow: drive stack %s: admit integration run: %v", stackID, serr)
			return
		}
		run, err = e.Repo.GetRun(ctx, res.RunID)
		if err != nil {
			log.Printf("workflow: drive stack %s: integration run %s: %v", stackID, res.RunID, err)
			return
		}
	}
	// Wait for the integration run to settle, delivering it under the auto
	// merge policy (and waiting for its PR to merge), then settle the plan
	// run; the wait doubles on transient delivery faults (STACK-2).
	e.waitForIntegrationSettle(ctx, run, compiled, stackID)
}

// waitForIntegrationSettle waits for the integration run to settle,
// delivering it under the auto merge policy and, under auto, waiting for its
// PR to merge, then settles the plan run. The wait between polls doubles when
// nothing progressed (a transient delivery fault leaving the run
// delivery_pending), so a persistently faulting integration delivery cannot
// burn one attempt every poll tick (STACK-2, 2026-08-16): 2s -> 4s -> 8s ->
// ... -> stackDriveMaxBackoff, reset to the base interval by any progressing
// pass.
func (e *Engine) waitForIntegrationSettle(ctx context.Context, run workflowledger.RunSnapshot, compiled *definition.CompiledWorkflow, stackID string) {
	autoPublish := compiled.Stacking.MergePolicy == "auto"
	backoff := stackDrivePollInterval
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		fresh, gerr := e.Repo.GetRun(ctx, run.RunID)
		if gerr != nil {
			log.Printf("workflow: drive stack %s: integration run %s: %v", stackID, run.RunID, gerr)
			return
		}
		progressed := false
		switch fresh.Status {
		case workflowledger.RunStatusSucceeded:
			if autoPublish && e.PR != nil && !stacking.ChunkRunNoDiff(ctx, e.Repo, fresh) {
				if !e.waitForPRMerged(ctx, fresh) {
					return // ctx done or oracle error; the stack stays resumable
				}
			}
			e.settlePlanRun(ctx, compiled, stackID)
			return
		case workflowledger.RunStatusDeliveryPending:
			if autoPublish {
				// Deliver through the engine's own machinery, NOT the gated
				// e.Deliver: the integration run is a derived run of THIS
				// stack (stable key <stack-id>:integration) - never the plan
				// run of a stack to refuse - and its decompose output can
				// legitimately re-plan the merged suite as mode=multi, which
				// the operator gate would misread as an undriven stack and
				// refuse forever. Mirrors the CLI's workflowStackDeliverRun
				// (= deliverRunWithStore) for the integration run.
				if _, derr := e.deliverPendingDirect(ctx, fresh); derr != nil {
					log.Printf("workflow: drive stack %s: deliver integration run: %v", stackID, derr)
				} else {
					progressed = true // the run moved: succeeded, delivery_failed, or reopened
				}
			} else {
				// grant policy: the integration run awaits its publish
				// grant (the host's auto-delivery or an explicit deliver);
				// the stack is complete and the plan run can settle.
				e.settlePlanRun(ctx, compiled, stackID)
				return
			}
		case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
			log.Printf("workflow: drive stack %s: integration run %s ended %s; run `mivia stack drive` to retry", stackID, fresh.RunID, fresh.Status)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if progressed {
			backoff = stackDrivePollInterval
		} else {
			backoff *= 2
			if backoff > stackDriveMaxBackoff {
				backoff = stackDriveMaxBackoff
			}
		}
	}
}

// waitForPRMerged polls the PR oracle until the run's branch merged, the
// context ends, or the oracle errors.
func (e *Engine) waitForPRMerged(ctx context.Context, run workflowledger.RunSnapshot) bool {
	for {
		merged, err := e.prMerged(ctx, run)
		if err != nil {
			log.Printf("workflow: drive stack: merge check integration run %s: %v", run.RunID, err)
			return false
		}
		if merged {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(stackDrivePollInterval):
		}
	}
}

// settlePlanRun resolves the plan run's own delivery_pending terminal once
// the stack is complete: with deliver_plan_run=false it settles succeeded
// (mirroring settlePlanRunSkippedDelivery) so the run does not park forever;
// with deliver_plan_run=true it stays delivery_pending for an explicit
// workflow_deliver (the chunk PRs already carry the reviewable work).
func (e *Engine) settlePlanRun(ctx context.Context, compiled *definition.CompiledWorkflow, runID string) {
	if compiled.Delivery != nil && compiled.Delivery.DeliverPlanRun {
		return // left for an explicit deliver
	}
	fresh, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		log.Printf("workflow: drive stack: settle plan run %s: %v", runID, err)
		return
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		return
	}
	now := time.Now()
	if err := e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
		log.Printf("workflow: drive stack: settle plan run %s: %v", runID, err)
		return
	}
	emitDeliveredRunFinished(runID)
}

// stackHasProgress reports whether the stack still has work the drive itself
// can advance: runs settling (running/implemented) or a published PR awaiting
// a merge the oracle can see. States that only external action can move - a
// reviewed chunk awaiting its publish grant, a reopened chunk whose terminal
// run only the operator drive re-admits, or a published PR with no merge
// oracle - stop the drive and leave the stack resumable (`mivia stack drive`
// carries the git+gh oracle and the grant flow). Waiting statuses (queued/
// planned/blocked/reopened) never count: a chunk is only waiting because a
// dependency has not merged, and that dependency is either in flight (counted
// above) or dead.
func stackHasProgress(byID map[string]tasks.Task, hasMergeOracle bool) bool {
	for _, t := range byID {
		switch t.Status {
		case stacking.StatusRunning, stacking.StatusImplemented:
			return true
		case stacking.StatusPublished:
			if hasMergeOracle {
				return true
			}
		case stacking.StatusFailed, stacking.StatusCanceled:
			// A failed chunk is terminal (the operator reconcile leaves
			// failed tasks alone) and blocks its dependents forever: the
			// drive must halt instead of polling an uncompletable stack
			// (STACK-2, 2026-08-16). A canceled chunk carries the same
			// verdict: it exists only because a dependency failed.
			return false
		}
	}
	return false
}

// isStackInFlightStatus reports whether a task status represents work in
// flight (a run processing, a PR awaiting merge, or a publish grant pending)
// rather than a settled state the drive can act on or leave alone.
func isStackInFlightStatus(status string) bool {
	switch status {
	case stacking.StatusQueued, stacking.StatusRunning, stacking.StatusImplemented, stacking.StatusReviewed, stacking.StatusPublished:
		return true
	default:
		return false
	}
}

// transitionStackTask transitions one chunk task, reporting whether the
// transition applied.
func transitionStackTask(ledger *tasks.Store, stackID, id, status string) bool {
	if err := ledger.TransitionTask(stackID, id, status); err != nil {
		log.Printf("workflow: drive stack %s: transition %s to %s: %v", stackID, id, status, err)
		return false
	}
	return true
}

// stackRunByKey returns the latest run admitted with a chunk's stable
// admission key, mirroring the CLI driver's stackRunRef (durable ledger
// lookup, so re-entry after a restart resolves the same runs).
func (e *Engine) stackRunByKey(ctx context.Context, stackID, chunkID string) (workflowledger.RunSnapshot, bool, error) {
	key, err := stacking.AdmissionKey(stackID, chunkID)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	runs, err := e.Repo.ListRuns(ctx)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// undrivenPlanRunReason returns a non-empty refusal reason when runID is the
// plan run of a multi-chunk stack whose drive has not completed, mirroring
// the CLI's classifyStackPlanRunDelivery + stackDriveCompleted. An empty
// result means the run is not a multi-chunk plan run, or the stack drove to
// completion, and delivery may proceed.
func (e *Engine) undrivenPlanRunReason(ctx context.Context, repo workflowledger.Repository, runID string, compiled *definition.CompiledWorkflow) string {
	if compiled == nil || compiled.Stacking == nil || !compiled.Stacking.Enabled {
		return ""
	}
	if _, ok := stacking.DecomposedChunks(ctx, repo, runID); !ok {
		return ""
	}
	if e.stackDriveCompleted(ctx, repo, runID, compiled) {
		return ""
	}
	return "is the plan run of a stack that has not fully driven yet: finish it with `mivia stack drive <workflow> --stack " + runID + "`, then settle the plan run with `mivia workflow deliver " + runID + "` - delivering it now would abandon the undriven stack while reporting the plan run succeeded"
}

// stackDriveCompleted reports whether the multi-chunk stack behind a plan run
// drove to completion: the plan output parsed, no incremental-decompose
// continuation pending, every chunk task merged, and the integration run
// admitted and settled (a delivery_pending integration run counts only under
// the grant merge policy - it awaits the publish grant; under auto the drive
// still merges it). Mirrors the CLI's stackDriveCompleted; without the task
// ledger (e.Store) the drive cannot be verified, so the gate refuses.
func (e *Engine) stackDriveCompleted(ctx context.Context, repo workflowledger.Repository, runID string, compiled *definition.CompiledWorkflow) bool {
	planOutput, err := stacking.LoadStackPlanOutput(ctx, repo, runID)
	if err != nil {
		return false
	}
	mode, chunks, hasMore, _, err := stacking.ParseStackPlanOutput(planOutput)
	if err != nil || mode != "multi" || len(chunks) == 0 {
		return false
	}
	if hasMore {
		return false
	}
	if e.Store == nil {
		return false
	}
	ledger := tasks.NewStore(e.Store)
	byID, err := stacking.TaskMap(ctx, ledger, runID)
	if err != nil {
		return false
	}
	if !stacking.AllChunksMerged(chunks, stacking.MergedSet(byID)) {
		return false
	}
	run, found, err := e.stackRunByKey(ctx, runID, stacking.IntegrationChunkID)
	if err != nil || !found {
		return false
	}
	switch run.Status {
	case workflowledger.RunStatusSucceeded:
		// Under merge_policy=auto with an oracle, a succeeded integration
		// run that actually pushed a diff must have its PR merged before the
		// plan run may publish: a crash between the drive's integration
		// delivery and its merge wait would leave the final PR open while
		// this gate reports the stack complete and an explicit
		// workflow_deliver publishes the plan run anyway (mirrors the CLI's
		// remoteMergeOracle gate, stack_drive_completed_test.go). The guard
		// is exactly waitForIntegrationSettle's - auto + oracle + durable
		// pushed evidence (not a confirmed no_diff): a no_diff run publishes
		// no PR and needs no merge, and approve policy and the no-oracle
		// degrade keep the pre-existing behavior. Fail closed: an oracle
		// error or a still-open PR both leave the stack resumable.
		if compiled.Stacking.MergePolicy == "auto" && e.PR != nil && !stacking.ChunkRunNoDiff(ctx, repo, run) {
			merged, err := e.prMerged(ctx, run)
			if err != nil || !merged {
				return false
			}
		}
		return true
	case workflowledger.RunStatusDeliveryPending:
		return compiled.Stacking.MergePolicy == "approve" // awaits the publish grant
	default:
		return false
	}
}
