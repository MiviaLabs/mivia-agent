package cli

// Real-git regression tests for stack merge detection (FIX: silent PR loss /
// never-completing stacks / closed-unmerged branches).
//
// The merge oracle must NOT treat a deleted branch as merged. It now verifies
// that the pushed head commit is an ancestor of the remote base branch, or (for
// squash/rebase merges) asks the remote host whether the PR is merged. A
// closed-unmerged PR whose branch was deleted must keep the stack waiting.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// gitRun runs git in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// scratchStackRepo builds a real git worktree with a bare origin remote so the
// merge checker can run against real git state.
func scratchStackRepo(t *testing.T) (root string, gc delivery.GitContext) {
	t.Helper()
	root = t.TempDir()
	gitRun(t, root, "init", "-b", "main")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "base")
	gitRun(t, root, "init", "--bare", filepath.Join(root, "origin.git"))
	gitRun(t, root, "remote", "add", "origin", filepath.Join(root, "origin.git"))
	gitRun(t, root, "push", "-u", "origin", "main")
	return root, delivery.GitContext{Dir: root, GitDir: filepath.Join(root, ".git")}
}

// seedDeliveryPendingRun admits a run row and settles it to delivery_pending
// through the valid pending->running->delivery_pending transitions.
func seedDeliveryPendingRun(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, snapshotJSON []byte) {
	t.Helper()
	run.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	step := func(to workflowledger.RunStatus) {
		t.Helper()
		stored, err := repo.GetRun(context.Background(), run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	step(workflowledger.RunStatusRunning)
	step(workflowledger.RunStatusDeliveryPending)
}

// seedStackTask stores the stack plan and its first running chunk task.
func seedStackTask(t *testing.T, ledger *tasks.Store, stackID, chunkID string) {
	t.Helper()
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(tasks.Task{ID: chunkID, PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusRunning}); err != nil {
		t.Fatal(err)
	}
}

// TestStackGitMergeNeverPushedBranchNotMerged: a branch that was never pushed
// has no commit and no pushed evidence. It must NOT read as merged.
func TestStackGitMergeNeverPushedBranchNotMerged(t *testing.T) {
	_, gc := scratchStackRepo(t)
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-never-pushed", "main", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Fatal("never-pushed branch read as merged: ref absence must require durable pushed evidence")
	}
}

// TestStackGitMergePushedBranchStillOnRemoteNotMerged: a delivered branch that
// is still present on origin (PR open) is not merged.
func TestStackGitMergePushedBranchStillOnRemoteNotMerged(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-open")
	if err := os.WriteFile(filepath.Join(root, "open.txt"), []byte("open\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "open.txt")
	gitRun(t, root, "commit", "-m", "open")
	gitRun(t, root, "push", "origin", "HEAD:refs/heads/wf/wt-open")
	headCommit := gitRun(t, root, "rev-parse", "wf/wt-open")
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-open", "main", headCommit, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Fatal("branch still present on origin read as merged")
	}
}

// TestStackGitMergeMergedOnRemoteCompletes: after a real merge into the base
// branch the head commit is an ancestor of origin/main, even if the remote
// branch is then deleted. The checker must report merged.
func TestStackGitMergeMergedOnRemoteCompletes(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-merged")
	if err := os.WriteFile(filepath.Join(root, "merged.txt"), []byte("merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "merged.txt")
	gitRun(t, root, "commit", "-m", "merged")
	headCommit := gitRun(t, root, "rev-parse", "wf/wt-merged")
	gitRun(t, root, "push", "-u", "origin", "wf/wt-merged")
	// Simulate a real merge on the remote: merge the head branch into main,
	// push main, then delete the head branch.
	gitRun(t, root, "checkout", "main")
	gitRun(t, root, "pull", "origin", "main")
	gitRun(t, root, "merge", "--no-ff", "-m", "merge wt-merged", "wf/wt-merged")
	gitRun(t, root, "push", "origin", "main")
	gitRun(t, filepath.Join(root, "origin.git"), "update-ref", "-d", "refs/heads/wf/wt-merged")

	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-merged", "main", headCommit, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("merged commit not detected as an ancestor of origin/main")
	}
}

// TestStackGitMergeClosedUnmergedBranchDeletedNotMerged: a human (or GitHub UI)
// closes a PR unmerged and deletes the branch. The branch is gone from origin
// but the commit never landed in the base branch, so the checker must NOT
// report merged.
func TestStackGitMergeClosedUnmergedBranchDeletedNotMerged(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-closed")
	if err := os.WriteFile(filepath.Join(root, "closed.txt"), []byte("closed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "closed.txt")
	gitRun(t, root, "commit", "-m", "closed")
	headCommit := gitRun(t, root, "rev-parse", "wf/wt-closed")
	gitRun(t, root, "push", "-u", "origin", "wf/wt-closed")
	// Close and delete the branch without merging.
	gitRun(t, filepath.Join(root, "origin.git"), "update-ref", "-d", "refs/heads/wf/wt-closed")

	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-closed", "main", headCommit, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Fatal("closed-unmerged deleted branch read as merged")
	}
}

// TestStackReconcileWithRealGitNeverPushedDeliversNotMerged drives the full
// reconcileStack over a real scratch repo: a delivery_pending run whose
// branch was never pushed must produce the publish-grant deliver action,
// never mark_merged — a stack must not complete with a missing PR.
func TestStackReconcileWithRealGitNeverPushedDeliversNotMerged(t *testing.T) {
	_, gc := scratchStackRepo(t)
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-never-pushed"
	seedStackTask(t, ledger, stackID, "a")
	run := workflowledger.RunSnapshot{
		RunID: "wfr-never-pushed", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
		WorktreeName: "wt-never-pushed", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	actions, err := reconcileStack(context.Background(), ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want exactly one", actions)
	}
	if actions[0].Action == stackActionMarkMerged {
		t.Fatalf("never-pushed delivery_pending chunk was marked merged (%+v): the stack must not complete without a PR", actions[0])
	}
	if actions[0].Action != stackActionDeliver {
		t.Fatalf("action = %q, want deliver (publish grant)", actions[0].Action)
	}
}

// TestStackReconcileDeliveryPendingWedgeUnwedges is the F9 regression: a
// chunk task orphaned at running with its run stuck at delivery_pending (the
// admitting process died, or the session driveCtx expired, between the run
// settling delivery_pending and driveChunk's own transition) must not stay
// wedged forever. reconcileTask already detected this state and returned
// deliver, but applyReconcileAction persisted nothing for it - the task
// never left running, which has no case in stackAwaitsGrantOnly's switch, so
// the merge-wait poll never took the durable-pause exit and looped until the
// context deadline. The fix: deliver now carries NewStatus=reviewed, which
// both applyReconcileAction persists and stackAwaitsGrantOnly recognizes.
func TestStackReconcileDeliveryPendingWedgeUnwedges(t *testing.T) {
	_, gc := scratchStackRepo(t)
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-orphaned-delivery"
	seedStackTask(t, ledger, stackID, "a")
	run := workflowledger.RunSnapshot{
		RunID: "wfr-orphaned", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
		WorktreeName: "wt-orphaned", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}

	actions, err := reconcileStack(context.Background(), ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Action != stackActionDeliver {
		t.Fatalf("actions = %+v, want exactly one deliver", actions)
	}
	if actions[0].NewStatus != stackStatusReviewed {
		t.Fatalf("deliver NewStatus = %q, want reviewed (or the task stays wedged at running)", actions[0].NewStatus)
	}

	// applyReconcileAction (called inside reconcileStack) must have actually
	// persisted the transition, not just decided it.
	task, err := ledger.GetTask(stackID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != stackStatusReviewed {
		t.Fatalf("task status after reconcile = %q, want reviewed", task.Status)
	}

	// The poll loop's durable-pause exit must now recognize the stack as
	// grant-only instead of looping: this is what actually breaks the wedge.
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	if !stackAwaitsGrantOnly(byID) {
		t.Fatalf("stackAwaitsGrantOnly = false after deliver->reviewed; the merge-wait poll would loop forever instead of pausing")
	}
}

// TestStackReconcileWithRealGitMergedOnRemoteCompletes drives reconcileStack
// over a delivered run whose commit is in the base branch: durable pushed
// evidence plus base ancestry must mark the chunk merged.
func TestStackReconcileWithRealGitMergedOnRemoteCompletes(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-merged")
	if err := os.WriteFile(filepath.Join(root, "merged.txt"), []byte("merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "merged.txt")
	gitRun(t, root, "commit", "-m", "merged")
	headCommit := gitRun(t, root, "rev-parse", "wf/wt-merged")
	gitRun(t, root, "push", "-u", "origin", "wf/wt-merged")
	// Merge into main on origin and delete the head branch.
	gitRun(t, root, "checkout", "main")
	gitRun(t, root, "pull", "origin", "main")
	gitRun(t, root, "merge", "--no-ff", "-m", "merge wt-merged", "wf/wt-merged")
	gitRun(t, root, "push", "origin", "main")
	gitRun(t, filepath.Join(root, "origin.git"), "update-ref", "-d", "refs/heads/wf/wt-merged")

	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-merged"
	seedStackTask(t, ledger, stackID, "a")
	run := workflowledger.RunSnapshot{
		RunID: "wfr-merged", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
		WorktreeName: "wt-merged", BaseRef: "main", RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "wfr-merged::digest",
		Status: "pushed", CommitSHA: headCommit, HeadRef: "wf/wt-merged",
	}); err != nil {
		t.Fatal(err)
	}
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	actions, err := reconcileStack(context.Background(), ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want exactly one", actions)
	}
	if actions[0].Action != stackActionMarkMerged {
		t.Fatalf("action = %q, want mark_merged for a genuinely merged run (%+v)", actions[0].Action, actions[0])
	}
}
