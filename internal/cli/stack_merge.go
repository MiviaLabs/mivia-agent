package cli

// Stack merge policy (plan D2/D3/D8, §5a policy B): wait loops that land the
// stack's PRs. Under merge_policy=auto the driver merges published chunk PRs
// itself (mark ready -> CI green -> squash-merge), and the integration PR is
// waited out to an actual git merge before the stack reports complete.

import (
	"context"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// waitForChunkMerges polls the reconcile loop until every chunk is merged,
// surfacing a terminal chunk failure as a stack halt. Reconcile is idempotent
// and marks a chunk merged as soon as git reports its PR branch merged, so a
// later drive pass naturally skips it and admits the next wave.
// stackMergePollInterval is how often the drive's merge-wait loops poll git
// merge state. A package var so integration tests can shorten it; production
// never assigns it (the same test-shortening convention as
// workflowReconcileInterval).
var stackMergePollInterval = 20 * time.Second

func waitForChunkMerges(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, checker MergeChecker, stackID string, chunks []ChunkPlan, policy string, stdout, stderr io.Writer) error {
	ticks := 0
	for {
		done, err := chunkMergePollPass(ctx, prepared, ledger, checker, stackID, chunks, policy, stdout, stderr)
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
		case <-time.After(stackMergePollInterval):
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
func chunkMergePollPass(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, checker MergeChecker, stackID string, chunks []ChunkPlan, policy string, stdout, stderr io.Writer) (done bool, err error) {
	repo := prepared.Repo
	actions, err := reconcileStack(ctx, ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		return false, fmt.Errorf("stack drive: reconcile: %w", err)
	}
	for _, a := range actions {
		if a.Action == stackActionMarkFailed {
			return false, haltStackForFailedChunk(ledger, stackID, a.TaskID, a.Note)
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
		return false, haltStackForFailedChunk(ledger, stackID, id, "")
	}
	// merge_policy=auto: publish the outstanding PRs' merges ourselves.
	if policy == "auto" {
		// A chunk reconcile just moved to reviewed (F9: a delivery_pending
		// run orphaned mid-drive) has no admission path back into driveChunk
		// - reviewed is not one of driveChunk's admissible pre-statuses, so
		// nothing else ever retries its delivery. Without this, an auto
		// stack would durably move the task to reviewed and then poll
		// forever anyway, same as the approve-policy wedge this pass fixes,
		// just without the grant-pause exit (stackAwaitsGrantOnly only
		// applies when policy != "auto").
		if err := autoDeliverReviewedChunks(ctx, prepared, repo, ledger, stackID, stdout, stderr); err != nil {
			return false, err
		}
		if err := autoMergePublishedChunks(ctx, prepared, repo, ledger, stackID, stdout, stderr); err != nil {
			return false, err
		}
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return false, err
	}
	// A chunk delivered (by a human's `mivia workflow deliver
	// --allow-publish` grant, or just landed since the last poll) may have
	// left a deferred commit (§5.2-5.3): admit its follow-up PR so the
	// stack does not report complete while it is still open.
	if err := admitPendingFollowUps(ctx, prepared, ledger, stackID, byID, stdout, stderr); err != nil {
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

// autoDeliverReviewedChunks retries delivery for every chunk task the
// reconciler moved to reviewed while its run is still delivery_pending (F9):
// a task orphaned mid-delivery (the admitting process died, or the session
// driveCtx expired, between the run settling delivery_pending and
// driveChunk's own transition) never re-enters driveChunk's admission CAS -
// reviewed is not a pre-admission status - so nothing else under
// merge_policy=auto ever attempts its delivery again. This mirrors
// driveChunk's own inline delivery block exactly (admitStackChunkRun's
// RunStatusDeliveryPending case), just re-entered from reconciled state
// instead of a fresh admission. A run not yet found, or already past
// delivery_pending (a concurrent human grant or an earlier pass already
// delivered it - reconcile will pick up the fresh status next pass), is not
// an error: the caller keeps polling.
func autoDeliverReviewedChunks(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, repo workflowledger.Repository, ledger *workflowledger.Store, stackID string, stdout, stderr io.Writer) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	for id, t := range byID {
		if t.Status != stackStatusReviewed {
			continue
		}
		run, found, err := stackRunRef(repo, stackID, id)
		if err != nil {
			return err
		}
		if !found || run.Status != workflowledger.RunStatusDeliveryPending {
			continue
		}
		if err := workflowStackDeliverRun(ctx, prepared.Root, prepared.Res, prepared.Store, prepared.Repo, run.RunID, true, false, stdout, stderr); err != nil {
			return fmt.Errorf("chunk %s auto-delivery failed: %w", id, err)
		}
		fresh, err := repo.GetRun(ctx, run.RunID)
		if err != nil {
			return fmt.Errorf("chunk %s: read run status after delivery: %w", id, err)
		}
		fmt.Fprintln(stdout, chunkSettleAfterDelivery(repo, ledger, stackID, id, fresh))
	}
	return nil
}

// autoMergePublishedChunks merges every published chunk PR for the stack
// (merge_policy=auto) that has no unmerged DEPENDENT - see
// blockedByUnmergedDependent. A PR that is not mergeable yet (checks
// pending/red, review requirements) reports why and is retried on the next
// poll; reconcile marks it merged the moment git reports the merge landed.
func autoMergePublishedChunks(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, repo workflowledger.Repository, ledger *workflowledger.Store, stackID string, stdout, stderr io.Writer) error {
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
		if err := autoMergeOne(ctx, prepared, repo, stackID, id, stdout, stderr); err != nil {
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
func blockedByUnmergedDependent(byID map[string]workflowledger.Task, chunkID string) (string, bool) {
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

// workflowStackMergePR is the pull-request merge boundary for
// merge_policy=auto (the driver merges published chunk PRs itself). A package
// var so integration tests can script merges against a local origin without a
// live gh host; production points at delivery.MergePullRequest.
var workflowStackMergePR = delivery.MergePullRequest

// workflowStackDeliverRun is the delivery boundary for the integration run.
// A package var so tests can stub delivery without standing up a full
// controller/workflow-run fixture; production points at cliworkflow.DeliverRunWithStore.
var workflowStackDeliverRun = cliworkflow.DeliverRunWithStore

// autoMergeOne resolves one chunk's PR (by its run's head branch) and merges
// it. No PR yet, or a merge refusal, is not an error: the wait loop retries.
func autoMergeOne(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, repo workflowledger.Repository, stackID, chunkID string, stdout, stderr io.Writer) error {
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
	ref, err := cliworkflow.WorkflowDeliverNewPR().FindByHead(ctx, slug, head)
	if err != nil {
		return fmt.Errorf("chunk %s: find PR: %w", chunkID, err)
	}
	if ref == nil {
		return nil // PR not visible yet; poll later
	}
	// Merge-time overlap guard (§guardChunkMergeOverlap): the last host
	// checkpoint before content lands on the base. A probe failure (network
	// down, missing refs) must NOT merge past the guard - an unevaluated
	// guard is not a passed guard - but it also must not halt the drive:
	// keep polling with the reason visible, so a transient failure heals on
	// the next pass and a persistent one names itself.
	base := prepared.Compiled.Delivery.Base
	gc := delivery.GitContext{Dir: prepared.Root, GitDir: filepath.Join(prepared.Root, ".git")}
	if err := guardChunkMergeOverlap(ctx, cliworkflow.WorkflowDeliverGit, gc, base, head, chunkID); err != nil {
		if errors.Is(err, errOverlapProbeFailed) {
			fmt.Fprintf(stdout, "chunk=%s overlap guard could not run; not merging this pass: %v\n", chunkID, err)
			return nil
		}
		return err
	}
	if err := workflowStackMergePR(ctx, slug, ref.RemoteID, ref.Draft); err != nil {
		if delivery.IsPermanentMergeError(err) {
			return fmt.Errorf("chunk %s PR %s permanent merge failure: %v", chunkID, ref.RemoteID, err)
		}
		fmt.Fprintf(stdout, "chunk=%s PR %s not mergeable yet: %v\n", chunkID, ref.RemoteID, err)
		return nil // keep polling; reconcile marks merged once it lands
	}
	fmt.Fprintf(stdout, "chunk=%s PR %s merged (or enqueued on the merge queue)\n", chunkID, ref.RemoteID)
	return nil
}

// waitIntegrationRunSettledFn is the seam for waitIntegrationRunSettled so tests
// can observe whether the integration gate admitted the run without running real
// delivery/merge plumbing.
var waitIntegrationRunSettledFn = waitIntegrationRunSettled

// waitIntegrationRunSettled finishes the last act of a completed stack: the
// integration run was admitted and settled by the drive pass; publish it when
// allowed and report the stack's terminal state. With merge_policy=auto the
// integration PR is merged and the merge waited out before the stack reports
// complete.
func waitIntegrationRunSettled(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, checker MergeChecker, stackID string, policy string, allowPublish bool, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(prepared.Repo, stackID, stackIntegrationChunkID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("stack %s: integration run was not admitted", stackID)
	}

	// Deliver the integration run when authorized. Under merge_policy=auto the
	// driver may publish without an explicit --allow-publish grant, matching
	// driveChunk's behavior; otherwise the caller must pass allowPublish=true.
	mayDeliver := policy == "auto" || allowPublish
	if run.Status == workflowledger.RunStatusDeliveryPending && mayDeliver {
		if err := workflowStackDeliverRun(ctx, prepared.Root, prepared.Res, prepared.Store, prepared.Repo, run.RunID, true, false, stdout, stderr); err != nil {
			return fmt.Errorf("integration run delivery failed: %w", err)
		}
		fresh, err := prepared.Repo.GetRun(ctx, run.RunID)
		if err != nil {
			return fmt.Errorf("integration run: read status after delivery: %w", err)
		}
		run = fresh
	}

	// A delivery that reopened the run for repair is not a terminal state:
	// the integration run must be resumed and re-delivered before the stack
	// can complete.
	if isResumableRunStatus(run.Status) {
		return fmt.Errorf("stack %s: integration run %s is %s after delivery; resume it with `mivia workflow resume %s` before the stack can complete", stackID, run.RunID, run.Status, run.RunID)
	}
	if run.Status == workflowledger.RunStatusDeliveryFailed {
		return fmt.Errorf("stack %s: integration run %s delivery failed; fix the refusal and resume or re-deliver before the stack can complete", stackID, run.RunID)
	}
	if workflowledger.IsTerminalRunStatus(run.Status) && run.Status != workflowledger.RunStatusSucceeded {
		return fmt.Errorf("stack %s: integration run %s is %s; repair or resume it before the stack can complete", stackID, run.RunID, run.Status)
	}

	// A no-diff integration run settles succeeded without ever pushing a branch.
	// There is no PR to merge, so the stack is complete immediately.
	if run.Status == workflowledger.RunStatusSucceeded && !stackRunPushed(prepared.Repo, run) {
		fmt.Fprintf(stdout, "stack %s complete: integration run=%s status=%s (no diff)\n", stackID, run.RunID, run.Status)
		return nil
	}

	// Under merge_policy=auto, merge the integration PR whenever it has durable
	// pushed evidence. This covers the in-function delivery above, an external
	// `mivia workflow deliver`, and a recovery sweep that delivered the run
	// before the driver reached this point. Without this, a later drive sees
	// run.Status=succeeded and skips autoMergeOne, leaving the final PR open.
	if policy == "auto" && stackRunPushed(prepared.Repo, run) {
		if err := autoMergeOne(ctx, prepared, prepared.Repo, stackID, stackIntegrationChunkID, stdout, stderr); err != nil {
			return err
		}
		return waitForIntegrationMerge(ctx, prepared, prepared.Repo, checker, stackID, policy, stdout, stderr)
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
// PR actually lands. Under merge_policy=auto it also re-attempts the merge
// each poll tick, matching the per-pass retry chunk PRs already get via
// autoMergePublishedChunks (F3).
func waitForIntegrationMerge(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, repo workflowledger.Repository, checker MergeChecker, stackID, policy string, stdout, stderr io.Writer) error {
	ticks := 0
	for {
		run, found, err := stackRunRef(repo, stackID, stackIntegrationChunkID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("stack %s: integration run disappeared", stackID)
		}
		head := stackHeadBranch(run)
		if head == "" {
			return fmt.Errorf("stack %s: integration run %s has no head branch to wait on", stackID, run.RunID)
		}
		runPushed := stackRunPushed(repo, run)
		// Under auto-merge policy, retry the integration PR merge every poll
		// tick: a transient merge refusal that outlives MergePullRequest's own
		// retry window must not leave the final PR open forever.
		if policy == "auto" && runPushed {
			if err := autoMergeOne(ctx, prepared, repo, stackID, stackIntegrationChunkID, stdout, stderr); err != nil {
				return err
			}
		}
		slug, _ := delivery.ParseOwnerRepo(run.RemoteURL)
		merged, err := checker.Merged(ctx, head, run.BaseRef, stackRunHeadCommit(repo, run), slug, runPushed)
		if err != nil {
			return err
		}
		if merged {
			fmt.Fprintf(stdout, "stack %s complete: integration PR merged (run=%s)\n", stackID, run.RunID)
			return nil
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
		case <-time.After(stackMergePollInterval):
		}
	}
}
