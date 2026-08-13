package cli

// Real-git regression tests for stack merge detection (FIX: silent PR loss /
// never-completing stacks).
//
// The stack merge poll previously treated ANY failure of
// `git rev-parse --verify -q refs/remotes/origin/<head>` as "merged". That is
// wrong in both directions:
//
//  1. A branch that was NEVER pushed has no tracking ref, so ref absence read
//     as merged and a delivery_pending chunk completed the stack with its PR
//     never created (silent PR loss). Merged must require durable pushed
//     evidence (a delivery record that reached pushed/succeeded, or observed
//     the tracking ref) before ref absence implies merged.
//  2. After a real remote merge the branch is deleted on origin, but nothing
//     prunes the local refs/remotes/origin/wf/* tracking ref, so Merged
//     returned false forever and auto-policy stacks never completed. Merged
//     must verify against the remote (ls-remote) instead of trusting the
//     stale local tracking ref.

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
// has no tracking ref and no remote ref. Ref absence must NOT read as merged:
// the PR was never created and the stack must keep waiting for delivery.
func TestStackGitMergeNeverPushedBranchNotMerged(t *testing.T) {
	_, gc := scratchStackRepo(t)
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-never-pushed", false)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Fatal("never-pushed branch read as merged: ref absence must require durable pushed evidence")
	}
}

// TestStackGitMergePushedBranchStillOnRemoteNotMerged: a delivered branch that
// is still present on origin (PR open) is not merged even when no local
// tracking ref exists (a plain push creates none).
func TestStackGitMergePushedBranchStillOnRemoteNotMerged(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-open")
	gitRun(t, root, "push", "origin", "HEAD:refs/heads/wf/wt-open")
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-open", true)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Fatal("branch still present on origin read as merged")
	}
}

// TestStackGitMergeMergedOnRemoteCompletes: after a real remote merge the
// branch is deleted on origin while the local tracking ref may persist
// (nothing prunes refs/remotes/origin/wf/*). The checker must still report
// merged so auto-policy stacks complete.
func TestStackGitMergeMergedOnRemoteCompletes(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-merged")
	gitRun(t, root, "push", "-u", "origin", "wf/wt-merged")
	// A merge UI deletes the branch on the remote only; the local tracking
	// ref is left behind (nothing prunes refs/remotes/origin/wf/*).
	gitRun(t, filepath.Join(root, "origin.git"), "update-ref", "-d", "refs/heads/wf/wt-merged")
	if _, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", "refs/remotes/origin/wf/wt-merged").Output(); err != nil {
		t.Fatal("test setup: the stale local tracking ref must persist after the remote delete")
	}
	if out, err := exec.Command("git", "-C", root, "ls-remote", "--heads", "origin", "wf/wt-merged").Output(); err != nil {
		t.Fatal(err)
	} else if len(out) != 0 {
		t.Fatalf("test setup: remote branch must be gone, ls-remote shows %q", out)
	}
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	merged, err := checker.Merged(context.Background(), "wf/wt-merged", true)
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("branch deleted on origin read as unmerged: a stale tracking ref must not pin the stack forever")
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
		WorktreeName: "wt-never-pushed",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	actions, err := reconcileStack(ledger, repo, checker, stackID, stackMaxChunkAttempts)
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

// TestStackReconcileWithRealGitMergedOnRemoteCompletes drives reconcileStack
// over a delivered run whose branch was merged and deleted on the remote:
// durable pushed evidence (a pushed delivery record) plus remote ref absence
// must mark the chunk merged so the stack completes and dependents admit.
func TestStackReconcileWithRealGitMergedOnRemoteCompletes(t *testing.T) {
	root, gc := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-merged")
	gitRun(t, root, "push", "-u", "origin", "wf/wt-merged")
	// Merge-UI deletion on the remote only; the local tracking ref persists.
	gitRun(t, filepath.Join(root, "origin.git"), "update-ref", "-d", "refs/heads/wf/wt-merged")

	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-merged"
	seedStackTask(t, ledger, stackID, "a")
	run := workflowledger.RunSnapshot{
		RunID: "wfr-merged", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
		WorktreeName: "wt-merged",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "wfr-merged::digest",
		Status: "pushed", CommitSHA: "deadbeef", HeadRef: "wf/wt-merged",
	}); err != nil {
		t.Fatal(err)
	}
	checker := gitMergeChecker{git: delivery.RealGit{}, gc: gc}
	actions, err := reconcileStack(ledger, repo, checker, stackID, stackMaxChunkAttempts)
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
