package cli

// Policy-A durable pause: when merge_policy is not "auto", the only actor
// that can advance a "reviewed" chunk is a human running `mivia workflow
// deliver <run> --allow-publish`. A wait loop over such a stack can never
// make progress by itself, so it must return cleanly (persist-and-exit; the
// tasks ledger is already the durable resume point) instead of polling and
// holding the plan run's execution flock while the human is away.

import (
	"errors"
	"fmt"
	"io"
	"sort"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// errStackAwaitsGrant reports a stack whose every unmerged chunk waits on a
// human publish grant. driveStackToCompletion treats it as a clean stop,
// mirroring waitIntegrationRunSettled's existing pause for the integration
// run.
var errStackAwaitsGrant = errors.New("stack awaits a human publish grant")

// stackAwaitsGrantOnly reports whether at least one chunk waits at
// "reviewed" and NOTHING else can advance without a human: every task is
// either reviewed or merged. A published chunk's PR can merge externally and
// a running chunk is still working, so either keeps the wait alive.
func stackAwaitsGrantOnly(byID map[string]tasks.Task) bool {
	reviewed := 0
	for _, t := range byID {
		switch t.Status {
		case stackStatusReviewed:
			reviewed++
		case stackStatusMerged:
		default:
			return false
		}
	}
	return reviewed > 0
}

// stackGrantHintLines returns one ready-to-paste guidance line per reviewed
// chunk. runRefByChunk resolves a chunk's run ID ("" when unknown).
func stackGrantHintLines(list []tasks.Task, runRefByChunk func(chunkID string) string) []string {
	var lines []string
	for _, t := range list {
		if t.Status != stackStatusReviewed {
			continue
		}
		ref := runRefByChunk(t.ID)
		if ref == "" {
			ref = "<run>"
		}
		lines = append(lines, fmt.Sprintf("chunk=%s awaits the publish grant: mivia workflow deliver %s --allow-publish", t.ID, ref))
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
