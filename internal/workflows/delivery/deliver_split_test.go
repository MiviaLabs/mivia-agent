package delivery

// Split delivery (spec-auto-split-oversized-prs.md §5.2-5.3, revised per
// §10): checkChunkDiffSize's own host-computed split decision (never an
// agent's claim) splits the fresh commit into a delivered commit (pushed)
// and a deferred commit (saved under DeferredBranchName, never pushed on
// the delivered branch). Uses the same real-git fixture as deliver_test.go,
// not a fake GitRunner - the git plumbing (add/reset/commit/branch/reset
// --hard) is exactly what's under test here.
//
// TestDeliverSplitsCommitWhenDeferredFilesPresent and
// TestDeliverNoSplitWhenDeferredFilesAbsent exercise
// freshDeliveryCommit(Split) directly via a pre-set InputDeferredFiles input,
// independent of how that input gets populated - the commit-splitting
// mechanics are the same regardless of source. The size-gate tests below
// exercise the real producer (checkChunkDiffSize's deterministic split) end
// to end: no test ever pre-sets InputDeferredFiles to influence the gate,
// because the gate does not honor a pre-existing value - only its own
// measurement decides.

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

func TestDeliverAutoSplitsOversizedDiffWhenEnabled(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// essential.txt alone is small; the.big.txt alone pushes the total over a
	// tiny hard_lines. No InputDeferredFiles is set anywhere - the host must
	// measure both files itself and decide to defer the larger one.
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "the.big.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if err != nil {
		t.Fatalf("Deliver: %v (want the host's own split to make the gate pass)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt (the host should have deferred the.big.txt)", delivered)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1", rec.StackRemainingCommits)
	}
}

func TestDeliverSizeGateOffByDefaultDespiteSplittableDiff(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "the.big.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	// policy.SplitDeferred left false (the opt-in default): a diff that
	// COULD be split must still be rejected outright, proving the gate
	// never splits without the workflow explicitly enabling it.

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (split_deferred is off)", err)
	}
}

func TestDeliverSizeGateStillRejectsWhenNothingSeparable(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// A single file exceeds hard_lines all on its own: there is nothing else
	// to defer, so a split cannot help and the gate must still refuse.
	writeWorktreeFile(t, worktreeRoot, "essential.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (a single oversized file has nothing separable to defer)", err)
	}
}

func TestDeliverSizeGateKeepsAtLeastOneFile(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// Two files, BOTH individually oversized: deferring the larger alone
	// still leaves the kept file over hard_lines, so the gate must refuse
	// rather than defer everything down to an empty delivered commit.
	writeWorktreeFile(t, worktreeRoot, "big.txt", strings.Repeat("line\n", 50))
	writeWorktreeFile(t, worktreeRoot, "also-big.txt", strings.Repeat("line\n", 20))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (the kept file alone still exceeds hard_lines)", err)
	}
}
