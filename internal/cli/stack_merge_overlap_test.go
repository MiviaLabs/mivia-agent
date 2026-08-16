package cli

// Pins the merge-time overlap guard (live finding, smoke-stack-3chunk-v3):
// all chunks of a stack delivered BEFORE any of them merged, so the
// delivery-time base-drift guard saw no base advance and passed every one.
// When two of those PRs changed the same task surface under different
// filenames, git merged both cleanly and master got duplicate definitions.
// Before auto-merging a chunk PR the driver must diff the PR branch against
// the base's advance since their merge-base: any common file means a sibling
// landed overlapping content after this PR was published, and the merge must
// halt the stack instead of double-merging.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeAndPush(t *testing.T, root, branch, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", file)
	gitRun(t, root, "commit", "-m", msg)
	gitRun(t, root, "push", "origin", branch)
}

func TestChunkMergeOverlapDetectsSiblingLandingSameFile(t *testing.T) {
	root, gc := scratchStackRepo(t)

	// Chunk branch changes shared.txt from the original base and publishes.
	gitRun(t, root, "checkout", "-b", "wf/c1")
	writeAndPush(t, root, "wf/c1", "shared.txt", "chunk c1\n", "c1: shared.txt")

	// A sibling merges its own change of shared.txt onto main afterwards.
	gitRun(t, root, "checkout", "main")
	writeAndPush(t, root, "main", "shared.txt", "sibling\n", "sibling: shared.txt")

	overlap, err := chunkMergeOverlap(context.Background(), workflowDeliverGit, gc, "main", "wf/c1")
	if err != nil {
		t.Fatalf("chunkMergeOverlap: %v", err)
	}
	if len(overlap) != 1 || overlap[0] != "shared.txt" {
		t.Fatalf("overlap = %v, want [shared.txt]", overlap)
	}
}

func TestChunkMergeOverlapEmptyWhenBaseAdvanceIsDisjoint(t *testing.T) {
	root, gc := scratchStackRepo(t)

	gitRun(t, root, "checkout", "-b", "wf/c2")
	writeAndPush(t, root, "wf/c2", "mine.txt", "chunk c2\n", "c2: mine.txt")

	gitRun(t, root, "checkout", "main")
	writeAndPush(t, root, "main", "other.txt", "sibling\n", "sibling: other.txt")

	overlap, err := chunkMergeOverlap(context.Background(), workflowDeliverGit, gc, "main", "wf/c2")
	if err != nil {
		t.Fatalf("chunkMergeOverlap: %v", err)
	}
	if len(overlap) != 0 {
		t.Fatalf("overlap = %v, want none for a disjoint base advance", overlap)
	}
}

func TestChunkMergeOverlapMissingHeadFailsClosed(t *testing.T) {
	// A head branch that never reached origin cannot be diffed, so the
	// guard has NO evidence. It must report the probe failure (fail
	// closed) instead of answering "no overlap": an unevaluated guard is
	// not a passed guard, and autoMergeOne must not merge past it.
	_, gc := scratchStackRepo(t)
	_, err := chunkMergeOverlap(context.Background(), workflowDeliverGit, gc, "main", "wf/never-pushed")
	if err == nil {
		t.Fatal("chunkMergeOverlap on a missing head returned nil error; want probe failure (fail closed)")
	}
}

// TestChunkMergeOverlapFetchFailureFailsClosed pins the asymmetric-transport
// hazard (F12 overlap-probe degradation): a failed fetch means BOTH refs may
// be stale, so the guard has nothing to evaluate. It must return a probe
// error, never a silent "no overlap" that lets a working gh merge path land
// content the guard never checked.
func TestChunkMergeOverlapFetchFailureFailsClosed(t *testing.T) {
	_, gc := scratchStackRepo(t)
	_, err := chunkMergeOverlap(context.Background(), errorGitRunner{err: errors.New("test: origin unreachable")}, gc, "main", "wf/c1")
	if err == nil {
		t.Fatal("chunkMergeOverlap on a failed fetch returned nil error; want probe failure (fail closed)")
	}
}
