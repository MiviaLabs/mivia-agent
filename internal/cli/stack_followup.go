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
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
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
func admitPendingFollowUps(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, byID map[string]tasks.Task, stdout, stderr io.Writer) error {
	for chunkID := range byID {
		if strings.HasSuffix(chunkID, "-deferred") {
			continue
		}
		if err := admitFollowUpsForChunk(prepared, ledger, stackID, chunkID, stdout, stderr); err != nil {
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
func allTasksMerged(byID map[string]tasks.Task) bool {
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
// call repeatedly and from multiple driver loop iterations: idempotent by
// construction (task-existence check) before any git/GitHub call.
func admitFollowUpsForChunk(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, chunkID string, stdout, stderr io.Writer) error {
	ctx := context.Background()
	run, found, err := stackRunRef(prepared.repo, stackID, chunkID)
	if err != nil || !found {
		return err
	}
	if !hasDeferredFollowUp(prepared.repo, run.RunID) {
		return nil
	}
	followUpID := deferredFollowUpChunkID(chunkID)
	if _, err := ledger.GetTask(stackID, followUpID); err == nil {
		return nil // already admitted
	}
	parentBranch := stackHeadBranch(run)
	if parentBranch == "" {
		return fmt.Errorf("chunk %s: delivered run has no worktree branch to derive a follow-up from", chunkID)
	}
	deferredBranch := delivery.DeferredBranchName(parentBranch)
	worktreeRoot, err := resolveRunWorktreeRoot(ctx, prepared.root, run)
	if err != nil {
		return fmt.Errorf("chunk %s: resolve worktree for follow-up: %w", chunkID, err)
	}
	gitCtx := delivery.GitContext{Dir: worktreeRoot, GitDir: worktreeRoot + "/.git"}
	if _, err := workflowDeliverGit.Run(ctx, gitCtx, "push", "origin", deferredBranch+":refs/heads/"+deferredBranch); err != nil {
		return fmt.Errorf("chunk %s: push deferred branch %s: %w", chunkID, deferredBranch, err)
	}
	slug, err := delivery.ParseOwnerRepo(run.RemoteURL)
	if err != nil {
		return fmt.Errorf("chunk %s: resolve repo for follow-up PR: %w", chunkID, err)
	}
	title, body := followUpPRMetadata(chunkID, run)
	ref, err := workflowDeliverNewPR().Create(ctx, slug, delivery.PRInput{
		Base: parentBranch, Head: deferredBranch, Title: title, Body: body, Draft: true,
	})
	if err != nil {
		return fmt.Errorf("chunk %s: create follow-up PR: %w", chunkID, err)
	}
	fmt.Fprintf(stdout, "chunk=%s follow-up PR %s %s opened (deferred scope, stacked on %s)\n", chunkID, ref.RemoteID, ref.URL, parentBranch)
	if err := registerFollowUpChunk(prepared.repo, ledger, stackID, chunkID, followUpID, deferredBranch, run, ref); err != nil {
		return fmt.Errorf("chunk %s: register follow-up %s: %w", chunkID, followUpID, err)
	}
	return nil
}

// hasDeferredFollowUp reports whether runID's most recent delivery record
// left a pending deferred commit (DeliveryRecord.StackRemainingCommits > 0).
func hasDeferredFollowUp(repo workflowledger.Repository, runID string) bool {
	records, err := repo.ListDeliveries(context.Background(), runID)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if rec.Status == "succeeded" && rec.StackRemainingCommits > 0 {
			return true
		}
	}
	return false
}

// followUpPRMetadata builds the follow-up PR's title/body from its parent
// chunk, with no agent involvement (the parent's own title/summary already
// explained the change; this just marks the split).
func followUpPRMetadata(chunkID string, run workflowledger.RunSnapshot) (title, body string) {
	title = fmt.Sprintf("deferred: %s follow-up (auto-split)", chunkID)
	body = fmt.Sprintf("Automatically split from chunk %s's delivery: this PR carries the scope the diff-size repair deferred to fit the stacking hard limit. Stacked on %s; merge after it.", chunkID, stackHeadBranch(run))
	return title, body
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

// registerFollowUpChunk writes the synthetic ledger state a follow-up chunk
// needs to be indistinguishable from a normal, already-delivered chunk to
// every existing reconcile/merge code path: a run row (CreateRun, CASed
// pending->running->succeeded, exactly the transitions ValidRunTransition
// allows), a delivery record proving it was pushed (stackRunPushed's
// evidence contract), and a task with Deps on the parent (so it participates
// in reconcileStack/stackTaskMap like any seeded chunk).
func registerFollowUpChunk(repo workflowledger.Repository, ledger *tasks.Store, stackID, parentChunkID, followUpID, deferredBranch string, parentRun workflowledger.RunSnapshot, ref delivery.PRRef) error {
	ctx := context.Background()
	key, err := stackAdmissionKey(stackID, followUpID)
	if err != nil {
		return err
	}
	runID := newCLIWorkflowRunID()
	worktreeName := parentRun.WorktreeName + "-deferred"
	snap := workflowledger.RunSnapshot{
		RunID: runID, InvocationKey: key,
		WorkflowName: parentRun.WorkflowName, WorkflowDigest: parentRun.WorkflowDigest,
		Status: workflowledger.RunStatusPending, ActiveStepID: "success",
		BaseRef: parentRun.BaseRef, BaseCommit: parentRun.BaseCommit,
		WorktreeName: worktreeName, RemoteURL: parentRun.RemoteURL,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		return fmt.Errorf("create follow-up run: %w", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusSucceeded} {
		cur, err := repo.GetRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("read follow-up run for CAS: %w", err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, next, nil); err != nil {
			return fmt.Errorf("settle follow-up run to %s: %w", next, err)
		}
	}
	rec := workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: delivery.DeliveryKey(runID, parentRun.WorkflowDigest),
		Mode: "draft", BaseRef: stackHeadBranch(parentRun), HeadRef: deferredBranch,
		Provider: "github", RemoteID: ref.RemoteID, URL: ref.URL, Status: "succeeded",
	}
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return fmt.Errorf("record follow-up delivery: %w", err)
	}
	task := tasks.Task{
		ID: followUpID, PlanRef: stackID, Scope: stackScope(stackID),
		Status: stackStatusPublished, Deps: []string{parentChunkID},
	}
	if err := ledger.CreateTask(task); err != nil {
		return fmt.Errorf("create follow-up task: %w", err)
	}
	return nil
}
