package cli

// Follow-up PR admission (spec-auto-split-oversized-prs.md §5.3): after a
// chunk's delivery leaves a deferred commit on its own local branch
// (delivery.DeferredBranchName), push that branch and open a stacked PR for
// it - no agent/workflow run needed, since the deferred commit already
// exists and was already covered by whatever gates ran before delivery. The
// follow-up is registered in the SAME stack's task/run ledger as a
// synthetic, already-delivered chunk, so every existing reconcile/merge
// code path (which is entirely run-and-task-ledger-keyed) handles it with
// no changes of its own.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// admitPendingFollowUps scans the stack's known chunks (byID, from
// stackTaskMap) for one whose delivery left a deferred commit, and admits a
// follow-up for each that doesn't have one yet. Called from
// driveStackToCompletion's loop (after every wave and every reconcile poll),
// so a follow-up is admitted whether its parent delivered synchronously
// (merge_policy=auto within a wave) or asynchronously (a human granted
// `mivia workflow deliver` for merge_policy=approve, discovered on the next
// poll). A follow-up's own id (suffix "-deferred") is skipped: it was
// created without going through checkChunkDiffSize/Deliver at all, so it
// can never itself declare deferred_files - no recursion is possible, but
// the skip keeps the scan cheap.
func admitPendingFollowUps(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, byID map[string]workflowledger.Task, stdout, stderr io.Writer) error {
	for chunkID := range byID {
		if strings.HasSuffix(chunkID, "-deferred") {
			continue
		}
		if err := admitFollowUpsForChunk(ctx, prepared, ledger, stackID, chunkID, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// allTasksMerged reports whether EVERY task known to the ledger (not just
// those named in a decompose-derived chunks slice) is merged. A follow-up
// PR admitted here is seeded directly into the ledger, not derived from any
// chunk plan, so allChunksMerged alone would consider the stack complete
// while a follow-up PR is still open; this closes that gap without needing
// to thread follow-ups back into the chunks slice everywhere it is passed.
func allTasksMerged(byID map[string]workflowledger.Task) bool {
	for _, t := range byID {
		if t.Status != stackStatusMerged {
			return false
		}
	}
	return true
}

// deferredFollowUpChunkID derives the follow-up chunk's task id from its
// parent, deterministic so admitFollowUpsForChunk is idempotent (a repeat
// call sees the task already exists and does nothing).
func deferredFollowUpChunkID(parentChunkID string) string {
	return parentChunkID + "-deferred"
}

// admitFollowUpsForChunk checks whether chunkID's delivery left a deferred
// commit (a StackRemainingCommits > 0 delivery record) and, if so and no
// follow-up has been admitted yet, pushes the deferred branch and opens a
// stacked PR for it (base = chunkID's own just-delivered branch). Safe to
// call repeatedly and from multiple driver loop iterations: the follow-up
// run row is reserved with a DETERMINISTIC run id BEFORE any git/GitHub
// call, so a retry or a concurrent admission resumes the same registration
// instead of duplicating run rows, delivery records, or PRs (bug 4).
func admitFollowUpsForChunk(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID, chunkID string, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(prepared.Repo, stackID, chunkID)
	if err != nil || !found {
		return err
	}
	followUpID := deferredFollowUpChunkID(chunkID)
	if _, err := ledger.GetTask(stackID, followUpID); err == nil {
		return nil // already admitted
	}
	// No deferred commit: the normal case for every non-split delivery. Check
	// BEFORE reserving the follow-up run row - the row is the durable fence
	// for a REAL deferred push, and reserving it for a plain delivery leaves
	// an orphan wfr-followup-* run row (snapshot "{}") that the recovery
	// sweep counts as a parked run and re-tries to resume forever ("workflow
	// snapshot digest does not match the admitted snapshot"; live finding,
	// 2026-08-16: every stack-drive IT sweep logged N parked runs plus
	// followup resumes skipped). The gate is the same one
	// delivery.EnsureFollowUpPublished uses (delivery record with
	// StackRemainingCommits > 0), which is durable by the time this runs -
	// the split record is written during delivery, before the follow-up
	// admission pass.
	if !delivery.HasDeferredFollowUp(ctx, prepared.Repo, run.RunID) {
		return nil
	}
	// The durable fence: reserve the follow-up run row (deterministic run
	// id) before any side effect. A crash after the PR was created, or a
	// concurrent admission, then resumes the SAME row instead of minting a
	// duplicate.
	runID, err := reserveFollowUpRun(prepared.Repo, stackID, followUpID, run)
	if err != nil {
		return fmt.Errorf("chunk %s: reserve follow-up run: %w", chunkID, err)
	}
	worktreeRoot, err := resolveRunWorktreeRoot(ctx, prepared.Root, run)
	if err != nil {
		return fmt.Errorf("chunk %s: resolve worktree for follow-up: %w", chunkID, err)
	}
	stdoutFn := func(s string) { fmt.Fprint(stdout, s) }
	deferredBranch, deferredSHA, ref, published, err := delivery.EnsureFollowUpPublished(ctx, cliworkflow.WorkflowDeliverGit, cliworkflow.WorkflowDeliverNewPR(), worktreeRoot, prepared.Repo, run, chunkID, stdoutFn)
	if err != nil {
		return fmt.Errorf("chunk %s: %w", chunkID, err)
	}
	if !published {
		return nil
	}
	if err := registerFollowUpChunk(prepared.Repo, ledger, stackID, chunkID, followUpID, runID, deferredBranch, deferredSHA, run, ref); err != nil {
		return fmt.Errorf("chunk %s: register follow-up %s: %w", chunkID, followUpID, err)
	}
	return nil
}

// followUpRunID derives the follow-up run id deterministically from its
// stable admission key (<stack>:<chunk>-deferred): the same key always
// yields the same run id, so CreateRun itself is the idempotency fence. The
// wfr-followup- prefix keeps the wfr- run-id prefix the run ledger's
// admission validation enforces.
func followUpRunID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "wfr-followup-" + hex.EncodeToString(sum[:16])
}

// reserveFollowUpRun durably reserves the follow-up run row BEFORE any
// git/GitHub side effect: this row is the admission's fence. The run id is
// deterministic, so a retry or a concurrent admission can never mint a
// second row - CreateRun returns ErrDuplicate and the caller resumes from
// the row this or an earlier reservation left.
func reserveFollowUpRun(repo workflowledger.Repository, stackID, followUpID string, parentRun workflowledger.RunSnapshot) (string, error) {
	ctx := context.Background()
	key, err := stackAdmissionKey(stackID, followUpID)
	if err != nil {
		return "", err
	}
	runID := followUpRunID(key)
	snap := workflowledger.RunSnapshot{
		RunID: runID, InvocationKey: key,
		WorkflowName: parentRun.WorkflowName, WorkflowDigest: parentRun.WorkflowDigest,
		Status: workflowledger.RunStatusPending, ActiveStepID: "success",
		BaseRef: parentRun.BaseRef, BaseCommit: parentRun.BaseCommit,
		WorktreeName: parentRun.WorktreeName + "-deferred", RemoteURL: parentRun.RemoteURL,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil && !errors.Is(err, workflowledger.ErrDuplicate) {
		return "", fmt.Errorf("create follow-up run: %w", err)
	}
	return runID, nil
}

// resolveRunWorktreeRoot resolves the filesystem path of runID's worktree,
// reusing the exact resolution path workflow_deliver.go uses (a run's
// worktree is not torn down on a successful delivery, so it is still
// present here).
func resolveRunWorktreeRoot(ctx context.Context, sourceRoot string, run workflowledger.RunSnapshot) (string, error) {
	identity, err := workflowspace.Resolve(ctx, sourceRoot, workflowspace.Identity{
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit,
		WorktreeName: run.WorktreeName, Branch: stackHeadBranch(run),
	})
	if err != nil {
		return "", err
	}
	return identity.Root, nil
}

// followUpRunRank orders the statuses a follow-up run row moves through
// (pending -> running -> succeeded). Anything else is treated as past every
// target so the settle loop never forces a transition away from it.
func followUpRunRank(status workflowledger.RunStatus) int {
	switch status {
	case workflowledger.RunStatusPending:
		return 0
	case workflowledger.RunStatusRunning:
		return 1
	case workflowledger.RunStatusSucceeded:
		return 2
	default:
		return 3
	}
}

// registerFollowUpChunk writes the synthetic ledger state a follow-up chunk
// needs to be indistinguishable from a normal, already-delivered chunk to
// every existing reconcile/merge code path: the run row already reserved by
// reserveFollowUpRun is CASed pending->running->succeeded (the transitions
// ValidRunTransition allows), a delivery record proves it was pushed
// (stackRunPushed's evidence contract), and a task with Deps on the parent
// makes it participate in reconcileStack/stackTaskMap like any seeded
// chunk. Every step is idempotent, so a retry resumes a crashed or
// concurrent registration instead of duplicating it.
func registerFollowUpChunk(repo workflowledger.Repository, ledger *workflowledger.Store, stackID, parentChunkID, followUpID, runID, deferredBranch, deferredSHA string, parentRun workflowledger.RunSnapshot, ref delivery.PRRef) error {
	ctx := context.Background()
	// Settle the reserved run row toward succeeded. A crashed or concurrent
	// admission may have left it at any status (pending, running, or
	// succeeded); a step already reached or passed is skipped (same-status
	// and backward edges are rejected by ValidRunTransition), and a CAS
	// conflict just means a concurrent admission advanced the row: re-read
	// and retry.
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusSucceeded} {
		for attempt := 0; attempt < 3; attempt++ {
			cur, err := repo.GetRun(ctx, runID)
			if err != nil {
				return fmt.Errorf("read follow-up run for CAS: %w", err)
			}
			if followUpRunRank(cur.Status) >= followUpRunRank(next) {
				break
			}
			if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, next, nil); err == nil {
				break
			} else if !errors.Is(err, workflowledger.ErrConflict) {
				return fmt.Errorf("settle follow-up run to %s: %w", next, err)
			}
		}
	}
	rec := workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: delivery.DeliveryKey(runID, parentRun.WorkflowDigest),
		Mode: "draft", BaseRef: stackHeadBranch(parentRun), HeadRef: deferredBranch,
		Provider: "github", RemoteID: ref.RemoteID, URL: ref.URL, Status: "succeeded",
		// stackRunPushed (the durable-pushed-evidence check reconcileTask
		// requires before ever marking a task merged) skips any record with
		// an empty CommitSHA - without this, a follow-up PR could merge on
		// GitHub and the stack would still loop forever reporting "publish
		// grant required" for a chunk that was never awaiting one.
		CommitSHA: deferredSHA,
	}
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return fmt.Errorf("record follow-up delivery: %w", err)
	}
	task := workflowledger.Task{
		ID: followUpID, PlanRef: stackID, Scope: stackScope(stackID),
		Status: stackStatusPublished, Deps: []string{parentChunkID},
	}
	if err := ledger.CreateTask(task); err != nil {
		return fmt.Errorf("create follow-up task: %w", err)
	}
	return nil
}
