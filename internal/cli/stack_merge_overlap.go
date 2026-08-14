package cli

// Merge-time overlap guard (live finding, smoke-stack-3chunk-v3): every
// chunk of a stack delivered BEFORE any of them merged, so the
// delivery-time base-drift guard legitimately passed each one, and two PRs
// carrying overlapping content merged cleanly into a broken base. The last
// point of host control is the auto-merge itself; it must refuse to merge a
// PR whose branch overlaps what landed on the base after their merge-base.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// chunkMergeOverlap returns the files changed BOTH by the head branch (since
// its merge-base with the base) and by the base's advance over that same
// merge-base. A non-empty result means a sibling landed overlapping content
// after this PR was published: merging it would duplicate or revert that
// work. Probe failures (missing refs, unreachable origin) degrade to "no
// overlap": the merge attempt itself then fails or waits, and a transient
// probe error must never halt the whole stack.
func chunkMergeOverlap(ctx context.Context, git delivery.GitRunner, gc delivery.GitContext, base, head string) ([]string, error) {
	if _, err := git.Run(ctx, gc, "fetch", "--quiet", "origin"); err != nil {
		return nil, nil
	}
	baseRef := "refs/remotes/origin/" + base
	headRef := "refs/remotes/origin/" + head
	mbOut, err := git.Run(ctx, gc, "merge-base", baseRef, headRef)
	if err != nil {
		return nil, nil
	}
	mergeBase := strings.TrimSpace(mbOut)
	if mergeBase == "" {
		return nil, nil
	}
	landed, err := nameOnlyDiff(ctx, git, gc, mergeBase, baseRef)
	if err != nil || len(landed) == 0 {
		return nil, nil
	}
	mine, err := nameOnlyDiff(ctx, git, gc, mergeBase, headRef)
	if err != nil {
		return nil, nil
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
// branch overlaps the base's advance since their merge-base. Returning an
// error (instead of the keep-polling nil of an unmergeable PR) is
// deliberate: the overlap never resolves by waiting, and auto-merging it
// would land duplicate implementations on the base.
func guardChunkMergeOverlap(ctx context.Context, git delivery.GitRunner, gc delivery.GitContext, base, head, chunkID string) error {
	overlap, err := chunkMergeOverlap(ctx, git, gc, base, head)
	if err != nil || len(overlap) == 0 {
		return nil
	}
	return fmt.Errorf("chunk %s: PR branch %s overlaps files already landed on %s since publication (%s); a sibling merged the same surface - rebase or re-scope the chunk before merging", chunkID, head, base, strings.Join(overlap, ", "))
}
