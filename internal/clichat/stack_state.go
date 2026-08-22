package clichat

// stack state: the stack driver's ledger queries, plan-output reconstruction,
// task seeding, reconcile sweep, and small state helpers. Kept out of
// stack_drive.go (the drive loop) so each file stays under the structure
// policy's line limits.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// loadStackPlanOutput reads the succeeded decompose step output of a
// plan-mode run from the run ledger (F1/F8: the plan is a run output).
func loadStackPlanOutput(repo workflowledger.Repository, stackID string) ([]byte, error) {
	return delivery.LoadStackPlanOutput(context.Background(), repo, stackID)
}

// loadAllStackChunks reconstructs the FULL chunk list a stack has planned
// across every already-admitted decompose wave: the plan run's own output
// (wave 0), plus any decompose-continuation runs (§12.1, waves 1..N) found
// in the run ledger. This is what lets a crashed-and-resumed `stack drive`
// see wave-2+ chunks a prior process already admitted - driveStack's
// dependency ordering and admission are derived entirely from the chunks
// slice it is called with, so a resumed process that only reconstructed
// wave 0 would never admit or drive the later waves' chunks again, even
// though they are already seeded in the task ledger. Returns the final
// hasMore/remainingScope from the LATEST wave found, so the caller knows
// whether to request yet another continuation.
func loadAllStackChunks(repo workflowledger.Repository, stackID string) (chunks []ChunkPlan, hasMore bool, remainingScope string, err error) {
	planOutput, err := loadStackPlanOutput(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	mode, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return nil, false, "", err
	}
	if mode != "multi" {
		return waveChunks, false, "", nil
	}
	chunks = append(chunks, waveChunks...)
	hasMore, remainingScope = waveHasMore, waveRemaining
	lastWave, err := latestDecomposeContinueWave(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	for wave := 1; wave <= lastWave; wave++ {
		run, found, err := stackDecomposeContinueRunRef(repo, stackID, wave)
		if err != nil {
			return nil, false, "", err
		}
		if !found {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d has an invocation key but no run", stackID, wave)
		}
		raw, err := loadStackPlanOutput(repo, run.RunID)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		_, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(raw)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		chunks = append(chunks, waveChunks...)
		hasMore, remainingScope = waveHasMore, waveRemaining
	}
	return chunks, hasMore, remainingScope, nil
}

// seedStackLedger records the plan artifact and the chunk tasks (D8); see
// delivery.SeedStackLedger. Re-entry is idempotent: existing tasks are left
// untouched and only missing tasks are created.
func seedStackLedger(ledger *workflowledger.Store, stackID string, chunks []ChunkPlan) error {
	return delivery.SeedStackLedger(context.Background(), ledger, stackID, chunks)
}

// chunkRunInputs builds the admission inputs and snapshot for one chunk-mode
// run (see delivery.ChunkRunInputs).
func chunkRunInputs(planInputs map[string]string, chunkID, prBase, stackPart string, plan *ChunkPlan, siblingFiles []string) (map[string]any, map[string]string) {
	return delivery.ChunkRunInputs(planInputs, chunkID, prBase, stackPart, plan, siblingFiles)
}

// stackPlanInputs reads the plan run's admitted snapshot and returns the
// workflow-declared inputs the chunks were decomposed from, so chunk runs can
// replay them (D3); see delivery.PlanInputs.
func stackPlanInputs(repo workflowledger.Repository, stackID string) (map[string]string, error) {
	return delivery.PlanInputs(context.Background(), repo, stackID)
}

// stackPRBase returns the delivery base branch the chunk PRs branch from:
// the workflow's delivery policy base (delivery honors pr_base, S4); see
// delivery.PRBase.
func stackPRBase(wf *definition.CompiledWorkflow) (string, error) {
	return delivery.PRBase(wf)
}

// applyReconcileActionFn stands for applyReconcileAction inside
// reconcileStack's task loop. Production keeps the direct call; tests may
// override it to force workflowledger.ErrTaskConflict, which through the
// serialized workflowledger.Store mutex only a genuinely concurrent writer
// can produce, so the skip-and-continue branch stays testable single-threaded.
var applyReconcileActionFn = applyReconcileAction

// reconcileStack applies the §5a recovery actions for every chunk task of
// the stack: task ledger x run ledger x git merge state, idempotently.
func reconcileStack(ctx context.Context, ledger *workflowledger.Store, repo workflowledger.Repository, checker MergeChecker, stackID string, maxAttempts int) ([]ReconcileAction, error) {
	list, err := ledger.ListTasksByScope(stackScope(stackID))
	if err != nil {
		return nil, err
	}
	var actions []ReconcileAction
	for _, t := range list {
		run, found, err := stackRunRef(repo, stackID, t.ID)
		if err != nil {
			return nil, err
		}
		info := RunInfo{Present: found}
		if found {
			info.Status = string(run.Status)
			info.ClaimStale = stackRunClaimStale(repo, run.RunID)
			info.NoDiff = chunkRunNoDiff(repo, run)
		}
		merged := false
		// Only a delivered run can be merged; a never-delivered run with a
		// missing remote ref must not be mistaken for a merge.
		if found && (run.Status == workflowledger.RunStatusDeliveryPending || run.Status == workflowledger.RunStatusSucceeded) {
			if head := stackHeadBranch(run); head != "" {
				runPushed := stackRunPushed(repo, run)
				slug, _ := delivery.ParseOwnerRepo(run.RemoteURL)
				merged, err = checker.Merged(ctx, head, run.BaseRef, stackRunHeadCommit(repo, run), slug, runPushed)
				if err != nil {
					return nil, err
				}
			}
		}
		t.Attempts = stackAttemptCount(ledger, stackID, t.ID)
		act := reconcileTask(t, info, merged, stackRunPushed(repo, run), maxAttempts)
		act.CurrentStatus = t.Status
		actions = append(actions, act)
		if err := applyReconcileActionFn(ledger, stackID, act); err != nil {
			if errors.Is(err, workflowledger.ErrTaskConflict) {
				continue // a concurrent writer already made this transition
			}
			return nil, err
		}
	}
	return actions, nil
}

// chunkRunNoDiff reports whether a run settled succeeded with a confirmed
// no_diff delivery outcome: the intended diff was empty, no PR was created,
// and the chunk is therefore complete. This requires POSITIVE evidence - an
// actual "no_diff" delivery record - not merely the absence of pushed
// evidence: a ListDeliveries read failure or a not-yet-recorded delivery
// also produce zero pushed records, and misreading either as "confirmed
// no_diff" durably marks a chunk merged with no PR ever created, silently
// dropping its content (an adversarial audit found this exact regression).
// A record that reached pushed/succeeded with a commit SHA always wins over
// a stale no_diff record from an earlier attempt on the same run.
func chunkRunNoDiff(repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	if run.Status != workflowledger.RunStatusSucceeded {
		return false
	}
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return false
	}
	sawNoDiff := false
	for _, rec := range records {
		switch rec.Status {
		case "pushed", "succeeded":
			if rec.CommitSHA != "" {
				return false
			}
		case "no_diff":
			sawNoDiff = true
		}
	}
	return sawNoDiff
}

// chunkSettleAfterDelivery transitions the chunk task after an in-drive
// delivery attempt and returns the status line the driver should print. A
// no_diff outcome marks the chunk merged; a real publish (fresh.Status ==
// succeeded, per chunkDeliverySucceeded) marks it published. cliworkflow.DeliverRunWithStore
// returns nil both on a real publish and on a repairable rejection that
// ReopenForRepair re-entered (the run settles back to running, not
// succeeded - see chunkDeliverySucceeded's doc comment and
// stack_deliver_repair_test.go for the live finding this guards against): a
// repair re-entry must NOT mark the chunk published, or the stack ledger
// permanently lies about a chunk with no PR at all, and the merge-wait loop
// polls a merge that can never land.
func chunkSettleAfterDelivery(repo workflowledger.Repository, ledger *workflowledger.Store, stackID, chunkID string, fresh workflowledger.RunSnapshot) string {
	if chunkRunNoDiff(repo, fresh) {
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusMerged)
		return fmt.Sprintf("chunk=%s has no diff; marking merged", chunkID)
	}
	if chunkDeliverySucceeded(string(fresh.Status)) {
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusPublished)
	}
	return chunkDeliveryOutcomeMessage(chunkID, fresh.RunID, string(fresh.Status))
}

// chunkSettleSucceeded transitions the chunk task when the run was already
// succeeded at admission. A no_diff outcome marks it merged and prints a
// message; otherwise it moves to implemented for normal delivery tracking.
func chunkSettleSucceeded(repo workflowledger.Repository, ledger *workflowledger.Store, stackID, chunkID string, snap workflowledger.RunSnapshot, stdout io.Writer) {
	if chunkRunNoDiff(repo, snap) {
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusMerged)
		fmt.Fprintf(stdout, "chunk=%s has no diff; marking merged\n", chunkID)
		return
	}
	_ = ledger.TransitionTask(stackID, chunkID, stackStatusImplemented)
}

// stackRunPushed reports durable pushed evidence for a chunk run: any of its
// delivery records reached pushed/succeeded with a commit SHA. A record in
// that state is only written after the branch was actually pushed to origin
// (the deliverer writes pushed after the push, succeeded after the PR is
// created). Without this evidence a missing remote ref means "never pushed",
// not "merged" - a delivery_pending run's PR may never have been created.
func stackRunPushed(repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return true
		}
	}
	return false
}

// stackRunHeadCommit returns the pushed commit SHA for a chunk run, if any.
// The commit is the durable evidence the merge oracle uses to verify that the
// base branch contains the change.
func stackRunHeadCommit(repo workflowledger.Repository, run workflowledger.RunSnapshot) string {
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return ""
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return rec.CommitSHA
		}
	}
	return ""
}

// stackTaskMap loads every stack task by id for the drive loop (see
// delivery.TaskMap).
func stackTaskMap(ledger *workflowledger.Store, stackID string) (map[string]workflowledger.Task, error) {
	return delivery.TaskMap(context.Background(), ledger, stackID)
}

// stackMergedSet returns the set of chunk ids whose tasks are merged (see
// delivery.MergedSet).
func stackMergedSet(byID map[string]workflowledger.Task) map[string]bool {
	return delivery.MergedSet(byID)
}

// allChunksMerged reports whether every chunk in the plan is merged (see
// delivery.AllChunksMerged).
func allChunksMerged(chunks []ChunkPlan, merged map[string]bool) bool {
	return delivery.AllChunksMerged(chunks, merged)
}

// chunkPartIndex returns the 0-based position of a chunk in dependency order,
// for the canonical "k/N" stack_part (see delivery.ChunkPartIndex).
func chunkPartIndex(chunkID string, order []string) (int, error) {
	return delivery.ChunkPartIndex(chunkID, order)
}

// isResumableRunStatus mirrors the ledger's resumable set for the driver.
func isResumableRunStatus(status workflowledger.RunStatus) bool {
	switch status {
	case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
		return true
	}
	return false
}

// stackRunClaimStale is the F7 liveness probe for an active-status chunk
// run: LinearController.Advance claims and heartbeats the run's execution
// claim for as long as its process is genuinely alive (every Advance call,
// not just the resume path), releasing it once Run returns - so an absent or
// expired claim means no process is currently driving this run, whether
// because the admitting process died mid-step or because the controller
// returned after parking at waiting_approval (a human hasn't approved yet).
// Both report stale=true on purpose: resuming a run that is only waiting on
// a human is a proven no-op (reconcileParkedResume's own doc comment - it
// re-attaches at the gate and re-parks, never re-executing or auto-approving
// anything), so treating it the same as a genuinely dead run costs nothing
// and closes the real F7 gap without a second code path. A probe failure
// (err != nil) degrades to "not stale": a transient storage fault must never
// be mistaken for a dead run.
func stackRunClaimStale(repo workflowledger.Repository, runID string) bool {
	_, acquiredAt, ok, err := repo.GetRunClaim(context.Background(), runID)
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	return time.Since(acquiredAt) > workflowledger.DefaultClaimLease
}
