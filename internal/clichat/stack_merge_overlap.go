package clichat

// Merge-time overlap guard (live finding, smoke-stack-3chunk-v3): every
// chunk of a stack delivered BEFORE any of them merged, so the
// delivery-time base-drift guard legitimately passed each one, and two PRs
// carrying overlapping content merged cleanly into a broken base. The last
// point of host control is the auto-merge itself; it must refuse to merge a
// PR whose branch overlaps what landed on the base after their merge-base.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// errOverlapProbeFailed marks a merge-overlap probe that could not run
// (fetch failure, missing refs, no merge-base): the guard has NO evidence,
// so the caller must not merge past it (an unevaluated guard is not a
// passed guard) - but must also not halt the drive over a probe that a
// later poll may satisfy.
var errOverlapProbeFailed = errors.New("overlap probe failed")

// chunkMergeOverlap returns the files changed BOTH by the head branch (since
// its merge-base with the base) and by the base's advance over that same
// merge-base. A non-empty result means a sibling landed overlapping content
// after this PR was published: merging it would duplicate or revert that
// work. A probe failure (failed fetch, missing refs, no merge-base) is
// returned as a non-nil error - the caller fails CLOSED, because a silent
// "no overlap" answer with no evidence behind it once let a working gh
// merge path land content past an unverified guard (F12 overlap-probe
// degradation, reachable when the git transport fails while gh works).
func chunkMergeOverlap(ctx context.Context, git delivery.GitRunner, gc delivery.GitContext, base, head string) ([]string, error) {
	if _, err := git.Run(ctx, gc, "fetch", "--quiet", "origin"); err != nil {
		return nil, fmt.Errorf("fetch origin: %w", err)
	}
	baseRef := "refs/remotes/origin/" + base
	headRef := "refs/remotes/origin/" + head
	mbOut, err := git.Run(ctx, gc, "merge-base", baseRef, headRef)
	if err != nil {
		return nil, fmt.Errorf("merge-base %s %s: %w", baseRef, headRef, err)
	}
	mergeBase := strings.TrimSpace(mbOut)
	if mergeBase == "" {
		return nil, errors.New("no merge-base between base and head")
	}
	landed, err := nameOnlyDiff(ctx, git, gc, mergeBase, baseRef)
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %w", mergeBase, baseRef, err)
	}
	if len(landed) == 0 {
		return nil, nil
	}
	mine, err := nameOnlyDiff(ctx, git, gc, mergeBase, headRef)
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %w", mergeBase, headRef, err)
	}
	var overlap []string
	for f := range mine {
		if landed[f] {
			overlap = append(overlap, f)
		}
	}
	sort.Strings(overlap)
	return overlap, nil
}

// nameOnlyDiff returns the set (and ordered list, via the map for the caller
// that needs membership) of files changed between from and to.
func nameOnlyDiff(ctx context.Context, git delivery.GitRunner, gc delivery.GitContext, from, to string) (map[string]bool, error) {
	out, err := git.Run(ctx, gc, "-c", "core.quotePath=false", "diff", "--name-only", from+".."+to)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			files[f] = true
		}
	}
	return files, nil
}

// guardChunkMergeOverlap halts the stack when the chunk's published PR
// branch overlaps the base's advance since their merge-base. A detected
// overlap returns a plain error (not the keep-polling nil of an unmergeable
// PR): the overlap never resolves by waiting, and auto-merging it would
// land duplicate implementations on the base. A probe failure wraps
// errOverlapProbeFailed so autoMergeOne can skip the merge for this pass
// while it keeps polling.
func guardChunkMergeOverlap(ctx context.Context, git delivery.GitRunner, gc delivery.GitContext, base, head, chunkID string) error {
	overlap, err := chunkMergeOverlap(ctx, git, gc, base, head)
	if err != nil {
		return fmt.Errorf("chunk %s: %w: cannot evaluate PR branch %s against base %s: %v", chunkID, errOverlapProbeFailed, head, base, err)
	}
	if len(overlap) == 0 {
		return nil
	}
	return fmt.Errorf("chunk %s: PR branch %s overlaps files already landed on %s since publication (%s); a sibling merged the same surface - rebase or re-scope the chunk before merging", chunkID, head, base, strings.Join(overlap, ", "))
}
