package delivery

// Split delivery (spec-auto-split-oversized-prs.md §5.2-5.3): a repair
// step's deferred_files decision splits the fresh commit into a delivered
// commit (pushed) and a deferred commit (saved under DeferredBranchName,
// never pushed on the delivered branch). Uses the same real-git fixture as
// deliver_test.go, not a fake GitRunner - the git plumbing (add/reset/
// commit/branch/reset --hard) is exactly what's under test here.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeliverSplitsCommitWhenDeferredFilesPresent(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "essential change\n")
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", "deferred change\n")

	deferredJSON, err := json.Marshal([]string{"deferred.txt"})
	if err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	inputs := map[string]string{"task": "split delivery", InputDeferredFiles: string(deferredJSON)}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), inputs))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}

	// The delivered commit (HEAD, and what got pushed) must carry ONLY
	// essential.txt - never deferred.txt.
	delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt", delivered)
	}
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin lacks refs/heads/wf/wt-test:\n%s", refs)
	}

	// The deferred branch must exist locally, contain ONLY deferred.txt on
	// top of the delivered commit, and never have reached origin.
	deferredBranch := DeferredBranchName("wf/wt-test")
	deferredDiff := runGitOut(t, worktreeRoot, "diff", "--name-only", "HEAD", deferredBranch)
	if deferredDiff != "deferred.txt" {
		t.Fatalf("deferred branch diff vs HEAD = %q, want exactly deferred.txt", deferredDiff)
	}
	parent := runGitOut(t, worktreeRoot, "rev-parse", deferredBranch+"^")
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	if parent != head {
		t.Fatalf("deferred branch parent = %q, want the delivered HEAD %q", parent, head)
	}
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); strings.Contains(refs, deferredBranch) {
		t.Fatalf("ls-remote origin has the deferred branch, want it never pushed:\n%s", refs)
	}

	// The delivery record must flag a pending follow-up.
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1", rec.StackRemainingCommits)
	}
}

func TestDeliverNoSplitWhenDeferredFilesAbsent(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "a.txt", "change\n")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 0 {
		t.Fatalf("StackRemainingCommits = %d, want 0 (no deferred_files, no split)", rec.StackRemainingCommits)
	}
	if branchExists(ctx, RealGit{}, gc, DeferredBranchName("wf/wt-test")) {
		t.Fatal("deferred branch must not exist when nothing was deferred")
	}
}

func TestDeliverExcludesDeferredFilesFromSizeGate(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// essential.txt alone is small; deferred.txt alone would push the total
	// over a tiny hard_lines - proving the gate measures ONLY the delivered
	// (non-deferred) portion.
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5

	deferredJSON, err := json.Marshal([]string{"deferred.txt"})
	if err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	inputs := map[string]string{"task": "gate", InputDeferredFiles: string(deferredJSON)}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err != nil {
		t.Fatalf("Deliver: %v (want the size gate to pass once deferred.txt is excluded)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}

func TestDeliverSizeGateStillRejectsOversizedEssentialFiles(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", strings.Repeat("line\n", 50))
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", "one line\n")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5

	deferredJSON, err := json.Marshal([]string{"deferred.txt"})
	if err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	inputs := map[string]string{"task": "gate", InputDeferredFiles: string(deferredJSON)}
	_, err = Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (deferring deferred.txt does not excuse an oversized essential slice)", err)
	}
}
