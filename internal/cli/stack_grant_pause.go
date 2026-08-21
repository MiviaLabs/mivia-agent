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

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
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

// stackingDriveAllowPublish reports whether a drive of a stacking workflow may
// publish chunk PRs itself: only under merge_policy=auto. Under approve (the
// default) the human publish grant is the single checkpoint - the drive marks
// chunks reviewed and pauses, exactly like the CLI foreground path without
// --allow-publish. The session hook and the recovery sweep both derive their
// publish authority from the policy (they must not hardcode the grant, or
// merge_policy=approve never pauses and the checkpoint is dead); the
// per-chunk `mivia workflow deliver --allow-publish` grant is the only
// channel that advances an approve stack.
func stackingDriveAllowPublish(compiled *definition.CompiledWorkflow) bool {
	return compiled != nil && compiled.Stacking != nil && compiled.Stacking.MergePolicy == "auto"
}

// stackAwaitsGrantOnly reports whether at least one chunk waits at
// "reviewed" (a human publish grant) or "published" (a human PR merge under
// a non-auto policy) and NOTHING else can advance without a human: every
// task is reviewed, published, merged, or a not-yet-admitted task (planned/
// queued/blocked/reopened). A running chunk is still working, so it keeps
// the wait alive. The pre-admission statuses count as "cannot advance"
// because the caller checks chunkNowAdmissible FIRST (a pre-admission chunk
// whose dependencies are all merged returns from the poll pass to drive the
// next wave); by the time this predicate runs, every pre-admission task is
// BLOCKED on an unmerged dependency - only a human merge can unlock it, so
// polling is a guaranteed no-op. A canceled chunk counts like a merged one:
// it is terminal and dead, so it neither waits for a human nor disables the
// pause. Without this, a seeded dependent chunk
// (stackAwaitsGrantOnly's old "default: return false") defeated the durable
// pause and the wait polled a grant-only stack until the drive bound (live
// finding: merge_policy=approve stacks with unadmitted dependents burned the
// whole attempt bound instead of pausing). This predicate is
// policy-agnostic; the caller applies it only when merge_policy != "auto"
// (under auto the driver itself merges published PRs, so polling does
// advance the stack).
func stackAwaitsGrantOnly(byID map[string]tasks.Task) bool {
	waiting := 0
	for _, t := range byID {
		switch t.Status {
		case stackStatusReviewed, stackStatusPublished, stackStatusPlanned, stackStatusQueued, stackStatusBlocked, stackStatusReopened:
			waiting++
		case stackStatusMerged, stackStatusCanceled, stackStatusFailed, stackStatusSkipped:
			// Terminal and dead (or done): none of these wait for a human or
			// defeat the durable pause. stackStatusFailed is currently
			// unreachable here in practice (anyChunkDurablyFailed halts the
			// drive before this runs), and no production path writes
			// stackStatusSkipped yet - both are listed defensively so this
			// switch stays a superset of stacking.TerminalStatuses instead of
			// a second, independently-maintained terminal list.
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
			if chunkHasUnmergedDeferredDependent(list, t.ID) {
				lines = append(lines, fmt.Sprintf("chunk=%s has an open PR AND a deferred follow-up that must merge first: merge the follow-up, then this chunk, then re-run the drive", t.ID))
			} else {
				lines = append(lines, fmt.Sprintf("chunk=%s has an open PR: merge it (merge_policy=approve), then re-run the drive", t.ID))
			}
		}
	}
	return lines
}

// chunkHasUnmergedDeferredDependent reports whether a follow-up chunk named
// <chunkID>-deferred exists, depends on chunkID, and is not yet merged. Under
// merge_policy=approve the parent PR must not merge before its follow-up,
// because the follow-up is stacked on the parent's branch and would be closed
// unmerged (orphaning its content) when the parent branch is deleted.
func chunkHasUnmergedDeferredDependent(list []tasks.Task, chunkID string) bool {
	byID := make(map[string]tasks.Task, len(list))
	for _, t := range list {
		byID[t.ID] = t
	}
	deferredID := chunkID + "-deferred"
	t, ok := byID[deferredID]
	if !ok || t.Status == stackStatusMerged {
		return false
	}
	for _, dep := range t.Deps {
		if dep == chunkID {
			return true
		}
	}
	return false
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

// chunkDeliverySucceeded reports whether a run's status, freshly read AFTER
// deliverRunWithStore returned a nil error, reflects a REAL publish. A nil
// error also covers a repairable rejection that ReopenForRepair re-entered
// (the run settles back to "running", not "succeeded") - see
// stack_deliver_repair_test.go for the live finding this guards against.
func chunkDeliverySucceeded(runStatus string) bool {
	return runStatus == "succeeded"
}

// chunkDeliveryOutcomeMessage reports the driver's post-delivery status line
// for a chunk, honest about whether it actually published or re-entered
// repair.
func chunkDeliveryOutcomeMessage(chunkID, runID, runStatus string) string {
	if chunkDeliverySucceeded(runStatus) {
		return fmt.Sprintf("chunk=%s published; merge queue will merge; waiting for the merge", chunkID)
	}
	return fmt.Sprintf("chunk=%s delivery rejected and entered repair (not published yet): mivia workflow resume %s", chunkID, runID)
}

// chunkNowAdmissible reports whether any chunk decompose declared, but that
// has not been admitted yet (still at a pre-admission status), now has every
// dependency merged. It mirrors nextAdmissionWave's readiness check
// (stackTaskReady) without needing topological order: the caller only needs
// to know whether SOMETHING is newly admissible, not the wave's sequencing.
func chunkNowAdmissible(chunks []ChunkPlan, byID map[string]tasks.Task) bool {
	merged := stackMergedSet(byID)
	for _, c := range chunks {
		t, ok := byID[c.ID]
		if !ok || !stackStatusIsAdmissiblePre(t.Status) {
			continue
		}
		if stackTaskReady(t, merged) {
			return true
		}
	}
	return false
}
