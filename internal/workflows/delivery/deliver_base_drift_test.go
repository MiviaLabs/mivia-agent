package delivery

// checkChunkBaseDrift pins a live e2e finding: a stacked chunk admitted
// against baseCommit, then delivered after a sibling merged a file this
// chunk also touches, must be refused - not silently published as a diff
// that reverts or duplicates the sibling's work (confirmed live: a chunk
// deleted a sibling's already-merged file and reimplemented another
// sibling's package with different content).

import (
	"context"
	"testing"
)

func TestDeliverRefusesChunkThatDriftedAgainstMergedSibling(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	// This chunk touches shared.txt.
	writeWorktreeFile(t, worktreeRoot, "shared.txt", "chunk change\n")

	// Simulate a sibling chunk merging shared.txt onto the remote base while
	// this chunk was in flight.
	runGit(t, repoRoot, "checkout", "main")
	writeWorktreeFile(t, repoRoot, "shared.txt", "sibling change\n")
	runGit(t, repoRoot, "add", "shared.txt")
	runGit(t, repoRoot, "commit", "-m", "sibling: change shared.txt")
	runGit(t, repoRoot, "push", "origin", "main")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "drift"}))
	if err == nil {
		t.Fatal("Deliver succeeded, want a rejection: this chunk's diff of shared.txt is stale against the sibling's merged change")
	}
	if IsRefusal(err) {
		t.Fatalf("Deliver error = %v (%T), want a plain repairable error, not a RefusalError (which bypasses the repair step)", err, err)
	}
}

// TestDeliverAllowsReconciledOverlapWithMergedSibling pins the repair route
// the drift guard's error text promises: when the chunk's staged content for
// an overlapped file already contains everything the sibling landed (a
// superset merge over the admitted base), merging the landed change onto the
// staged content is a no-op and delivery must pass. The name-based overlap
// alone must not reject it - that made the repair hint unsatisfiable.
func TestDeliverAllowsReconciledOverlapWithMergedSibling(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, _, originURL, run, repo := newDeliveryFixture(t)

	// Give the admitted base a shared.txt, and point the chunk worktree at
	// that commit as its admitted base.
	runGit(t, repoRoot, "checkout", "main")
	writeWorktreeFile(t, repoRoot, "shared.txt", "line1\nline3\n")
	runGit(t, repoRoot, "add", "shared.txt")
	runGit(t, repoRoot, "commit", "-m", "base: add shared.txt")
	runGit(t, repoRoot, "push", "origin", "main")
	admittedBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	runGit(t, worktreeRoot, "reset", "--hard", admittedBase)

	// The sibling inserts a line in the middle of shared.txt.
	writeWorktreeFile(t, repoRoot, "shared.txt", "line1\nsibling\nline3\n")
	runGit(t, repoRoot, "add", "shared.txt")
	runGit(t, repoRoot, "commit", "-m", "sibling: extend shared.txt")
	runGit(t, repoRoot, "push", "origin", "main")

	// The chunk reconciles: its staged content carries the sibling's line
	// plus its own append - a clean superset merge over the admitted base.
	writeWorktreeFile(t, worktreeRoot, "shared.txt", "line1\nsibling\nline3\nchunk\n")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, admittedBase, originURL, policy, map[string]string{"task": "reconciled"}))
	if err != nil {
		t.Fatalf("Deliver: %v, want success (staged content already contains the sibling's landed change)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}

func TestDeliverAllowsChunkWhenBaseAdvancedWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	writeWorktreeFile(t, worktreeRoot, "chunk.txt", "chunk change\n")

	// A sibling merges an UNRELATED file - no overlap, must not be refused.
	runGit(t, repoRoot, "checkout", "main")
	writeWorktreeFile(t, repoRoot, "unrelated.txt", "sibling change\n")
	runGit(t, repoRoot, "add", "unrelated.txt")
	runGit(t, repoRoot, "commit", "-m", "sibling: add unrelated.txt")
	runGit(t, repoRoot, "push", "origin", "main")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "no-drift"}))
	if err != nil {
		t.Fatalf("Deliver: %v, want success (no file overlap with the sibling's merge)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}
