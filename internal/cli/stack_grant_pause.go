package cli

// Policy-A durable pause: when merge_policy is not "auto", the only actors
// that can advance the stack are humans - a "reviewed" chunk needs `mivia
// workflow deliver <run> --allow-publish`, a "published" chunk (its PR
// already open, e.g. a diff-size split's follow-up) needs a manual merge. A
// wait loop over such a stack can never make progress by itself, so it must
// return cleanly (persist-and-exit; the tasks ledger is already the durable
// resume point) instead of polling and holding the plan run's execution
// flock while the human is away.

import (
	"errors"
	"fmt"
	"io"
	"sort"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// errStackAwaitsGrant reports a stack whose every unmerged chunk waits on a
// human action: a publish grant (`mivia workflow deliver <run>
// --allow-publish`) for a reviewed chunk, or a PR merge under
// merge_policy=approve for a published one. driveStackToCompletion treats it
// as a clean stop, mirroring waitIntegrationRunSettled's existing pause for
// the integration run.
var errStackAwaitsGrant = errors.New("stack awaits a human publish grant or merge")

// stackAwaitsGrantOnly reports whether at least one chunk waits at
// "reviewed" (a human publish grant) or "published" (a human PR merge under
// a non-auto policy) and NOTHING else can advance without a human: every
// task is reviewed, published, or merged. A running chunk is still working,
// so it keeps the wait alive. This predicate is policy-agnostic; the caller
// applies it only when merge_policy != "auto" (under auto the driver itself
// merges published PRs, so polling does advance the stack).
func stackAwaitsGrantOnly(byID map[string]tasks.Task) bool {
	waiting := 0
	for _, t := range byID {
		switch t.Status {
		case stackStatusReviewed, stackStatusPublished:
			waiting++
		case stackStatusMerged:
		default:
			return false
		}
	}
	return waiting > 0
}

// stackGrantHintLines returns one ready-to-paste guidance line per waiting
// chunk: reviewed chunks get the deliver command, published chunks get the
// human-merge instruction (merge_policy=approve). runRefByChunk resolves a
// chunk's run ID ("" when unknown).
func stackGrantHintLines(list []tasks.Task, runRefByChunk func(chunkID string) string) []string {
	var lines []string
	for _, t := range list {
		switch t.Status {
		case stackStatusReviewed:
			ref := runRefByChunk(t.ID)
			if ref == "" {
				ref = "<run>"
			}
			lines = append(lines, fmt.Sprintf("chunk=%s awaits the publish grant: mivia workflow deliver %s --allow-publish", t.ID, ref))
		case stackStatusPublished:
			lines = append(lines, fmt.Sprintf("chunk=%s has an open PR: merge it (merge_policy=approve), then re-run the drive", t.ID))
		}
	}
	return lines
}

// printStackGrantPause writes the pause guidance for every reviewed chunk.
func printStackGrantPause(repo workflowledger.Repository, stackID string, byID map[string]tasks.Task, stdout io.Writer) {
	list := make([]tasks.Task, 0, len(byID))
	for _, t := range byID {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, line := range stackGrantHintLines(list, func(chunkID string) string {
		run, found, err := stackRunRef(repo, stackID, chunkID)
		if err != nil || !found {
			return ""
		}
		return run.RunID
	}) {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "stack %s paused: re-run the drive after granting; state is durable\n", stackID)
}

// anyChunkDurablyFailed reports whether the stack has a chunk task at
// stackStatusFailed right now, regardless of which pass produced it.
func anyChunkDurablyFailed(ledger *tasks.Store, stackID string) (string, bool) {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return "", false
	}
	for id, t := range byID {
		if t.Status == stackStatusFailed {
			return id, true
		}
	}
	return "", false
}
