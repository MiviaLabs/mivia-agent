package cli

// Stack merge policy (plan D2/D3/D8, §5a policy B): wait loops that land the
// stack's PRs. Under merge_policy=auto the driver merges published chunk PRs
// itself (mark ready -> CI green -> squash-merge), and the integration PR is
// waited out to an actual git merge before the stack reports complete.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// waitForChunkMerges polls the reconcile loop until every chunk is merged,
// surfacing a terminal chunk failure as a stack halt. Reconcile is idempotent
// and marks a chunk merged as soon as git reports its PR branch merged, so a
// later drive pass naturally skips it and admits the next wave.
func waitForChunkMerges(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, checker MergeChecker, stackID string, chunks []ChunkPlan, policy string, stdout, stderr io.Writer) error {
	const pollInterval = 20 * time.Second
	ticks := 0
	for {
		done, err := chunkMergePollPass(prepared, ledger, checker, stackID, chunks, policy, stdout, stderr)
		if err != nil || done {
			return err
		}
		ticks++
		if ticks%3 == 0 {
			fmt.Fprintf(stdout, "stack %s: waiting for chunk merges to land...\n", stackID)
		}
		// The poll must honor the caller's context: a cancelled/expired ctx
		// (the session attempt bound) stops the drive instead of sleeping
		// through the poll forever and holding the run's execution flock.
		select {
		case <-ctx.Done():
			return fmt.Errorf("stack drive: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// chunkMergePollPass runs one iteration of the merge-wait poll: apply the
// merge policy, reconcile, and decide whether the wait is over. done=true
// with err=nil covers three distinct exits the caller must not keep polling
// past: full completion, a dependent chunk newly admissible (the outer
// driveStackToCompletion loop must re-drive), and errStackAwaitsGrant (a
// durable pause). Split out of waitForChunkMerges to keep that function
// under the file-size gate's function-length cap.
func chunkMergePollPass(prepared *preparedWorkflowRun, ledger *tasks.Store, checker MergeChecker, stackID string, chunks []ChunkPlan, policy string, stdout, stderr io.Writer) (done bool, err error) {
	repo := prepared.repo
	// merge_policy=auto: publish the outstanding PRs' merges ourselves.
	if policy == "auto" {
		if err := autoMergePublishedChunks(prepared, repo, ledger, stackID, stdout, stderr); err != nil {
			return false, err
		}
	}
	actions, err := reconcileStack(ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		return false, fmt.Errorf("stack drive: reconcile: %w", err)
	}
	for _, a := range actions {
		if a.Action == stackActionMarkFailed {
			return false, fmt.Errorf("stack %s halted: chunk %s failed terminally (%s)", stackID, a.TaskID, a.Note)
		}
	}
	// A chunk already durably failed (from an EARLIER pass, not this one)
	// produces no fresh mark-failed action on resume - reconcileTask leaves
	// a terminal failed task alone every pass - so the halt above only
	// fires on the transition INTO failed, never on resuming a stack that
	// already has one. Without this check the wait polled every 20s
	// forever: not merged (the failed chunk never will be) and not
	// grant-only (failed isn't reviewed/merged), an adversarial audit found.
	if id, failed := anyChunkDurablyFailed(ledger, stackID); failed {
		return false, fmt.Errorf("stack %s halted: chunk %s failed terminally", stackID, id)
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return false, err
	}
	// A chunk delivered (by a human's `mivia workflow deliver
	// --allow-publish` grant, or just landed since the last poll) may have
	// left a deferred commit (§5.2-5.3): admit its follow-up PR so the
	// stack does not report complete while it is still open.
	if err := admitPendingFollowUps(prepared, ledger, stackID, byID, stdout, stderr); err != nil {
		return false, fmt.Errorf("stack drive: %w", err)
	}
	byID, err = stackTaskMap(ledger, stackID)
	if err != nil {
		return false, err
	}
	if allChunksMerged(chunks, stackMergedSet(byID)) && allTasksMerged(byID) {
		return true, nil
	}
	// A not-yet-admitted chunk's dependencies just merged: this pass cannot
	// advance it any further (admission only happens in driveStack, called
	// by the OUTER loop after this function's caller returns) - polling
	// again would repeat the same no-op forever. Live deadlock this fixed:
	// the wait only used to return once EVERY chunk decompose declared
	// showed merged, but a dependent chunk never shows merged until it is
	// admitted, and it is never admitted until the wait returns and the
	// outer loop's `continue` reaches driveStack again - a dependency chain
	// past the first wave hung forever, even under merge_policy=auto with a
	// live merge queue.
	if chunkNowAdmissible(chunks, byID) {
		return true, nil
	}
	// Policy A durable pause (§stackAwaitsGrantOnly): when only human
	// publish grants can advance the stack, polling is a guaranteed no-op.
	// Persist-and-exit: print the grant guidance and stop; the ledger is
	// the resume point.
	if policy != "auto" && stackAwaitsGrantOnly(byID) {
		printStackGrantPause(repo, stackID, byID, stdout)
		return false, errStackAwaitsGrant
	}
	return false, nil
}

// autoMergePublishedChunks merges every published chunk PR for the stack
// (merge_policy=auto) that has no unmerged DEPENDENT - see
// blockedByUnmergedDependent. A PR that is not mergeable yet (checks
// pending/red, review requirements) reports why and is retried on the next
// poll; reconcile marks it merged the moment git reports the merge landed.
func autoMergePublishedChunks(prepared *preparedWorkflowRun, repo workflowledger.Repository, ledger *tasks.Store, stackID string, stdout, stderr io.Writer) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	for id, t := range byID {
		if t.Status != stackStatusPublished {
			continue
		}
		if blocker, blocked := blockedByUnmergedDependent(byID, id); blocked {
			fmt.Fprintf(stdout, "chunk=%s merge deferred: dependent chunk %s has not merged yet\n", id, blocker)
			continue
		}
		if err := autoMergeOne(prepared, repo, stackID, id, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// blockedByUnmergedDependent reports whether chunkID has an UNMERGED
// FOLLOW-UP dependent (a diff-size split's follow-up chunk - see
// registerFollowUpChunk, always named "<chunkID>-deferred"). A follow-up's PR
// is based on its parent's own branch (delivery.EnsureFollowUpPublished), not
// master: merging the parent first squash-merges and deletes that base
// branch, and GitHub does not reliably retarget a squash-merged PR's
// dependents (a live smoke test confirmed the base branch simply disappears
// and the dependent PR closes unmerged, orphaning its content). Every
// follow-up must land before its parent is allowed to merge.
//
// This must NOT match a normal decompose dependency edge (chunk B declares
// depends_on: [A]): that edge only gates B's ADMISSION until A merges - it
// does not mean A must wait for B. An adversarial audit found the original
// unqualified Deps scan treated every such edge as follow-up-shaped and
// deadlocked auto-merge on any stack with a genuine dependency: A publishes,
// this function finds unmerged B depending on A and defers A's merge
// forever, while B can never be admitted because A never merges.
func blockedByUnmergedDependent(byID map[string]tasks.Task, chunkID string) (string, bool) {
	for id, t := range byID {
		if id != chunkID+"-deferred" {
			continue
		}
		for _, dep := range t.Deps {
			if dep == chunkID && t.Status != stackStatusMerged {
				return id, true
			}
		}
	}
	return "", false
}

// autoMergeOne resolves one chunk's PR (by its run's head branch) and merges
// it. No PR yet, or a merge refusal, is not an error: the wait loop retries.
func autoMergeOne(prepared *preparedWorkflowRun, repo workflowledger.Repository, stackID, chunkID string, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(repo, stackID, chunkID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	head := stackHeadBranch(run)
	if head == "" {
		return nil
	}
	slug, err := delivery.ParseOwnerRepo(run.RemoteURL)
	if err != nil {
		return fmt.Errorf("chunk %s: resolve repo: %w", chunkID, err)
	}
	ref, err := workflowDeliverNewPR().FindByHead(context.Background(), slug, head)
	if err != nil {
		return fmt.Errorf("chunk %s: find PR: %w", chunkID, err)
	}
	if ref == nil {
		return nil // PR not visible yet; poll later
	}
	// Merge-time overlap guard (§guardChunkMergeOverlap): the last host
	// checkpoint before content lands on the base.
	base := prepared.compiled.Delivery.Base
	gc := delivery.GitContext{Dir: prepared.root, GitDir: filepath.Join(prepared.root, ".git")}
	if err := guardChunkMergeOverlap(context.Background(), workflowDeliverGit, gc, base, head, chunkID); err != nil {
		return err
	}
	if err := delivery.MergePullRequest(context.Background(), slug, ref.RemoteID, ref.Draft); err != nil {
		fmt.Fprintf(stdout, "chunk=%s PR %s not mergeable yet: %v\n", chunkID, ref.RemoteID, err)
		return nil // keep polling; reconcile marks merged once it lands
	}
	fmt.Fprintf(stdout, "chunk=%s PR %s merged (or enqueued on the merge queue)\n", chunkID, ref.RemoteID)
	return nil
}

// waitIntegrationRunSettled finishes the last act of a completed stack: the
// integration run was admitted and settled by the drive pass; publish it when
// allowed and report the stack's terminal state. With merge_policy=auto the
// integration PR is merged and the merge waited out before the stack reports
// complete.
func waitIntegrationRunSettled(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, checker MergeChecker, stackID string, policy string, allowPublish bool, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(prepared.repo, stackID, stackIntegrationChunkID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("stack %s: integration run was not admitted", stackID)
	}
	if run.Status == workflowledger.RunStatusDeliveryPending && allowPublish {
		if err := deliverRunWithStore(ctx, prepared.root, prepared.res, prepared.store, prepared.repo, run.RunID, true, false, stdout, stderr); err != nil {
			return fmt.Errorf("integration run delivery failed: %w", err)
		}
	}
	if policy == "auto" && run.Status == workflowledger.RunStatusDeliveryPending {
		if err := autoMergeOne(prepared, prepared.repo, stackID, stackIntegrationChunkID, stdout, stderr); err != nil {
			return err
		}
		return waitForIntegrationMerge(ctx, prepared.repo, checker, stackID, stdout, stderr)
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		fmt.Fprintf(stdout, "stack %s complete; integration run awaits the publish grant: mivia workflow deliver %s --allow-publish\n", stackID, run.RunID)
		return nil
	}
	fmt.Fprintf(stdout, "stack %s complete: integration run=%s status=%s\n", stackID, run.RunID, run.Status)
	return nil
}

// waitForIntegrationMerge polls git until the integration PR's branch is
// merged into the base, so the stack reports complete only after the final
// PR actually lands.
func waitForIntegrationMerge(ctx context.Context, repo workflowledger.Repository, checker MergeChecker, stackID string, stdout, stderr io.Writer) error {
	const pollInterval = 20 * time.Second
	ticks := 0
	for {
		run, found, err := stackRunRef(repo, stackID, stackIntegrationChunkID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("stack %s: integration run disappeared", stackID)
		}
		if head := stackHeadBranch(run); head != "" {
			merged, err := checker.Merged(context.Background(), head, stackRunPushed(repo, run))
			if err != nil {
				return err
			}
			if merged {
				fmt.Fprintf(stdout, "stack %s complete: integration PR merged (run=%s)\n", stackID, run.RunID)
				return nil
			}
		}
		ticks++
		if ticks%3 == 0 {
			fmt.Fprintf(stdout, "stack %s: waiting for the integration PR merge to land...\n", stackID)
		}
		// The poll must honor the caller's context: a cancelled/expired ctx
		// (the session attempt bound) stops the drive instead of sleeping
		// through the poll forever and holding the run's execution flock.
		select {
		case <-ctx.Done():
			return fmt.Errorf("stack drive: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
