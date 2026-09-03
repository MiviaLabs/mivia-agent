package cliworkflow

import (
	"context"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// SkipParkedPlanRunPublication reports whether a delivery_pending run has the
// SKIP SHAPE the sweep may settle WITHOUT publishing: the compiled workflow
// disables the plan run's own publication (delivery.deliver_plan_run false -
// the default) and the run's task ledger carries the seeded stack plan the
// chunk drive wrote. The shape predicate alone does NOT authorize the settle:
// reconcileParkedDelivery settles succeeded only when the stack actually drove
// to completion (StackDriveCompleted); a seeded-but-incomplete stack stays
// delivery_pending for the operator to finish with 'mivia stack drive'. Any
// resolution failure (missing run, corrupt snapshot) returns false, so the run
// falls through to DeliverRunWithStore, which reports the error exactly as
// before.
func SkipParkedPlanRunPublication(ctx context.Context, store *storage.SQLite, repo workflowledger.Repository, runID string) bool {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return false
	}
	_, compiled, _, err := ValidateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return false
	}
	if compiled == nil || compiled.Delivery == nil || compiled.Delivery.DeliverPlanRun {
		return false
	}
	_, err = workflowledger.NewStore(store).ReadBackPlan(runID)
	return err == nil
}

// StackPlanMergePolicy resolves the stacking merge_policy of a plan run from
// its admitted snapshot (the same snapshot ValidateWorkflowResumeSnapshot
// validates on the resume path), so StackDriveCompleted can apply the
// policy-aware delivery_pending rule. Any resolution failure (missing run,
// corrupt snapshot) returns "" - the grant default: a delivery_pending
// integration run then counts as complete (admitted, awaiting the publish
// grant).
func StackPlanMergePolicy(ctx context.Context, repo workflowledger.Repository, runID string) string {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return ""
	}
	_, compiled, _, err := ValidateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return ""
	}
	if compiled == nil || compiled.Stacking == nil {
		return ""
	}
	return compiled.Stacking.MergePolicy
}

// StackDriveCompleted reports whether a stacking plan run's chunk stack
// actually drove to completion: the run ledger carries the succeeded decompose
// output the driver reads (LoadStackPlanOutputFunc), every chunk task is merged
// (AllChunksMergedFunc over StackTaskMapFunc), and the final integration run - keyed
// by its stable admission key; runID IS the stack id - was admitted and
// settled. The gate is deliberately STRICTER than waitIntegrationRunSettled
// (which reports complete for an unsettled integration run), so the sweep
// never settles a plan run over a stack the driver is still advancing. A
// delivery_pending integration run counts as settled ONLY under the grant
// merge policy ("awaits the publish grant"); under merge_policy=auto the
// driver still auto-merges the integration PR, so delivery_pending is NOT
// complete there. Any resolution failure (missing run, corrupt output,
// unseeded plan) returns false: a seeded-but-incomplete stack stays
// delivery_pending for 'mivia stack drive' to finish.
//
// remoteMergeOracle: settle paths (deliver, stack drive, the sweep) MUST
// pass true - for a succeeded, pushed integration run under auto policy,
// the oracle (git merge-base, then gh IsMerged if inconclusive) must
// confirm the PR actually merged before the plan run may settle.
// Read-only display surfaces (workflow status's undriven notice, the
// run_finished event publisher) pass false: the durable pushed evidence
// settles the DISPLAY verdict, and a read-only surface runs no probes.
func StackDriveCompleted(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID, policy string, remoteMergeOracle bool) bool {
	// LoadAllStackChunksFunc (not the plan run's own output alone) so a pending
	// decompose-continuation wave (§12.1: hasMore=true with no continuation
	// admitted yet) is never mistaken for a complete stack just because every
	// CURRENTLY KNOWN chunk merged.
	chunks, hasMore, _, err := LoadAllStackChunksFunc(repo, runID)
	if err != nil {
		return false
	}
	if hasMore {
		return false
	}
	byID, err := StackTaskMapFunc(workflowledger.NewStore(store), runID)
	if err != nil {
		return false
	}
	if !AllChunksMergedFunc(chunks, StackMergedSetFunc(byID)) {
		return false
	}
	// The final full-suite integration run must have been admitted and
	// settled - a STRICTER gate than waitIntegrationRunSettled, which
	// reports the stack complete even for an unsettled integration run
	// (conservative by design). runID IS the stack id here, so the
	// integration run resolves by its stable admission key
	// <stack-id>:integration.
	intRun, found, err := StackRunRefFunc(repo, runID, delivery.IntegrationChunkID)
	if err != nil || !found {
		return false
	}
	switch intRun.Status {
	case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
		return false
	case workflowledger.RunStatusDeliveryPending:
		// delivery_pending is complete only under the grant policy: the
		// driver reports the stack complete there awaiting the publish
		// grant, while under merge_policy=auto it still auto-merges the
		// integration PR (waitIntegrationRunSettled) - settling now would
		// break that contract (a later drive skips autoMergeOne once the
		// integration run is terminal).
		return policy != "auto"
	case workflowledger.RunStatusSucceeded:
		// Under merge_policy=auto with durable pushed evidence, the
		// integration PR must actually be merged before the plan run may
		// settle. Otherwise a crash between DeliverRunWithStore and
		// waitForIntegrationMerge leaves the final PR open while the
		// sweep reports the stack complete.
		if policy != "auto" || !StackRunPushedFunc(repo, intRun) {
			return true
		}
		if !remoteMergeOracle {
			// Display-only caller: the durable pushed evidence settles the
			// verdict, and a read-only surface must not run network probes.
			return true
		}
		slug, _ := delivery.ParseOwnerRepo(intRun.RemoteURL)
		merged, err := GitMergeCheckFunc(ctx, WorkflowDeliverGit, WorkflowDeliverNewPR(), delivery.GitContext{Dir: root, GitDir: filepath.Join(root, ".git")}, StackHeadBranchFunc(intRun), intRun.BaseRef, StackRunHeadCommitFunc(repo, intRun), slug, true)
		if err != nil {
			return false
		}
		return merged
	case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled,
		workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
		// Terminal failure: the integration run settled but the stack did NOT
		// complete. A failed stack must not make the plan run look complete.
		return false
	default:
		// Unknown status: fail closed. Only the explicit statuses above are
		// recognized as stack-complete.
		return false
	}
}
