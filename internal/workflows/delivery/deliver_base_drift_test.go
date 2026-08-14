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
