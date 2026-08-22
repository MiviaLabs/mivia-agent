package cli

// Incremental, wave-scoped decompose (§12.1 of
// docs/architecture/spec-auto-split-oversized-prs.md): the invocation-key
// derivation and run-ledger lookups a decompose-continuation wave needs.
// Split out of stack_reconcile.go to keep that file under the repo's
// per-file line ceiling (.mivia/policy/go-structure.json).

import (
	"context"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// decomposeContinuePrefix namespaces decompose-continuation invocation keys
// so they can never collide with a real chunk's "<stack-id>:<chunk-id>" key
// (chunkIDRE forbids colons in a chunk id, so no chunk id can ever equal
// "decompose:N").
const decomposeContinuePrefix = ":decompose:"

// stackDecomposeContinueKey derives the stable invocation key for wave N's
// decompose-continuation run (§12.1): re-admission after a restart resolves
// to the SAME run, exactly like stackAdmissionKey does for chunk runs.
func stackDecomposeContinueKey(stackID string, wave int) (string, error) {
	if strings.TrimSpace(stackID) == "" {
		return "", fmt.Errorf("stack id must not be empty")
	}
	if wave < 1 {
		return "", fmt.Errorf("decompose continuation wave must be >= 1 (got %d)", wave)
	}
	return fmt.Sprintf("%s%s%d", stackID, decomposeContinuePrefix, wave), nil
}

// stackDecomposeContinueRunRef finds the run whose stable invocation key is
// wave N's decompose-continuation key, mirroring stackRunRef.
func stackDecomposeContinueRunRef(repo workflowledger.Repository, stackID string, wave int) (workflowledger.RunSnapshot, bool, error) {
	key, err := stackDecomposeContinueKey(stackID, wave)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// latestDecomposeContinueWave scans the stack's run ledger for the highest
// already-admitted decompose-continuation wave number, or 0 if none. Used to
// resume an interrupted incremental-decompose sequence at the right wave
// instead of re-admitting wave 1 (stackDecomposeContinueKey's stable key
// makes re-admission idempotent either way, but starting from the right wave
// avoids a wasted lookup per already-completed wave on every resume).
func latestDecomposeContinueWave(repo workflowledger.Repository, stackID string) (int, error) {
	prefix := stackID + decomposeContinuePrefix
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return 0, err
	}
	best := 0
	for _, r := range runs {
		if !strings.HasPrefix(r.InvocationKey, prefix) {
			continue
		}
		n, convErr := strconv.Atoi(strings.TrimPrefix(r.InvocationKey, prefix))
		if convErr != nil {
			continue
		}
		if n > best {
			best = n
		}
	}
	return best, nil
}

// loadAllStackChunksForDrive reconstructs the full chunk list across every
// already-admitted decompose wave for a drive (runStackDrive). Unlike the
// strict loadAllStackChunks (kept for the reconcile sweep), it recovers a
// wedged continuation wave instead of failing on it: a wave whose newest run
// has no succeeded decompose output is replayed from an older succeeded run
// under the same key (the wave's chunks are already seeded from that
// output), re-admitted with a fresh run when the newest run settled failed,
// or skipped with an actionable message naming the run id while the run is
// still live (pending/running - resumable). A skipped wave suppresses further
// wave admission (hasMore=false) so the drive never auto-requests a wave out
// of order; the operator resolves the live run and re-runs `mivia stack
// drive`. The wave-0 output is passed in (already loaded by the caller).
func loadAllStackChunksForDrive(prepared *cliworkflow.PreparedWorkflowRun, stackID string, planOutput []byte, planInputs map[string]string, stdout, stderr io.Writer) (chunks []ChunkPlan, hasMore bool, hasUnsettledWave bool, remainingScope string, err error) {
	mode, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return nil, false, false, "", err
	}
	if mode != "multi" {
		return waveChunks, false, false, "", nil
	}
	chunks = append(chunks, waveChunks...)
	hasMore, remainingScope = waveHasMore, waveRemaining
	lastWave, err := latestDecomposeContinueWave(prepared.Repo, stackID)
	if err != nil {
		return nil, false, false, "", err
	}
	skippedWave := false
	for wave := 1; wave <= lastWave; wave++ {
		run, found, rerr := stackDecomposeContinueRunRef(prepared.Repo, stackID, wave)
		if rerr != nil {
			return nil, false, false, "", rerr
		}
		if !found {
			return nil, false, false, "", fmt.Errorf("stack %s: decompose continuation wave %d has an invocation key but no run", stackID, wave)
		}
		raw, lerr := loadStackPlanOutput(prepared.Repo, run.RunID)
		if lerr == nil {
			_, waveChunks, waveHasMore, waveRemaining, perr := parseStackPlanOutput(raw)
			if perr != nil {
				return nil, false, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, perr)
			}
			chunks = append(chunks, waveChunks...)
			hasMore, remainingScope = waveHasMore, waveRemaining
			continue
		}
		waveChunks, waveHasMore, waveRemaining, skipped, rerr := recoverDecomposeContinueWave(prepared, stackID, wave, run, remainingScope, planInputs, stdout, stderr)
		if rerr != nil {
			return nil, false, false, "", rerr
		}
		if skipped {
			skippedWave = true
			continue
		}
		chunks = append(chunks, waveChunks...)
		hasMore, remainingScope = waveHasMore, waveRemaining
	}
	hasUnsettledWave = skippedWave
	if prepared.Compiled != nil {
		if err := enforceMaxTotalChunks(prepared.Compiled.Stacking, stackID, len(chunks)); err != nil {
			return nil, false, false, "", err
		}
	}
	return chunks, hasMore, hasUnsettledWave, remainingScope, nil
}

// enforceMaxTotalChunks refuses a chunk list that exceeds the workflow's
// max_total_chunks cap (0 or nil config = uncapped). The cap used to be
// checked only AFTER a continuation wave was admitted; the error path then
// left the wave's chunks unseeded, and the next drive replayed and seeded
// them anyway - silently exceeding the cap. The drive loader and the wave
// admission sites both call this, so the cap holds whether the chunks arrive
// from a fresh admission or a re-drive's wave replay.
func enforceMaxTotalChunks(stacking *definition.StackingConfig, stackID string, have int) error {
	if stacking == nil || stacking.MaxTotalChunks <= 0 || have <= stacking.MaxTotalChunks {
		return nil
	}
	return fmt.Errorf("stack %s has %d chunks across admitted decompose waves, exceeding max_total_chunks=%d; delete the excess wave runs or raise the cap", stackID, have, stacking.MaxTotalChunks)
}

// recoverDecomposeContinueWave handles one decompose-continuation wave whose
// newest run has no succeeded decompose output. A live (pending/running)
// run is skipped with an actionable message naming the run id and its
// resumability; a terminal run is replayed from an older succeeded run under
// the same key (that output is what seeded the wave's chunks) or re-admitted
// with a fresh run (same stable key; the newest run wins the run-ref lookup
// on the next drive). Every path either recovers the wave or reports exactly
// which run to resume or delete - never the bare 'plan run %q has no
// succeeded decompose output' wedge error.
func recoverDecomposeContinueWave(prepared *cliworkflow.PreparedWorkflowRun, stackID string, wave int, run workflowledger.RunSnapshot, remainingScope string, planInputs map[string]string, stdout, stderr io.Writer) (chunks []ChunkPlan, hasMore bool, nextRemainingScope string, skipped bool, err error) {
	if isResumableRunStatus(run.Status) {
		fmt.Fprintf(stderr, "stack %s: decompose continuation wave %d run=%s is %s (resumable); resume it with `mivia workflow resume %s` or wait for it to settle, then re-run `mivia stack drive`\n", stackID, wave, run.RunID, run.Status, run.RunID)
		return nil, false, "", true, nil
	}
	older, found, rerr := succeededDecomposeContinueRunRef(prepared.Repo, stackID, wave)
	if rerr != nil {
		return nil, false, "", false, rerr
	}
	if found {
		raw, lerr := loadStackPlanOutput(prepared.Repo, older.RunID)
		if lerr != nil {
			return nil, false, "", false, fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, lerr)
		}
		_, waveChunks, waveHasMore, waveRemaining, perr := parseStackPlanOutput(raw)
		if perr != nil {
			return nil, false, "", false, fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, perr)
		}
		return waveChunks, waveHasMore, waveRemaining, false, nil
	}
	switch run.Status {
	case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
		// Re-admittable: the wave produced no output, so re-admit it with a
		// fresh run under the same stable key (newest wins the run-ref
		// lookup, so the recovery run supersedes the failed one).
	default:
		fmt.Fprintf(stderr, "stack %s: decompose continuation wave %d run=%s settled at %s with no succeeded decompose output; resume or delete run %s, then re-run `mivia stack drive`\n", stackID, wave, run.RunID, run.Status, run.RunID)
		return nil, false, "", true, nil
	}
	nextChunks, nextHasMore, nextRemaining, aerr := stackDecomposeContinueAdmit(prepared, stackID, wave, remainingScope, planInputs, stdout, stderr)
	if aerr != nil {
		fmt.Fprintf(stderr, "stack %s: decompose continuation wave %d run=%s settled at %s with no succeeded decompose output; automatic re-admission failed: %v; resume or delete run %s (`mivia workflow resume %s` / `mivia workflow delete %s`), then re-run `mivia stack drive`\n", stackID, wave, run.RunID, run.Status, aerr, run.RunID, run.RunID, run.RunID)
		return nil, false, "", true, nil
	}
	return nextChunks, nextHasMore, nextRemaining, false, nil
}

// succeededDecomposeContinueRunRef finds the newest run whose stable
// invocation key is wave N's decompose-continuation key AND whose decompose
// output is loadable (a succeeded decompose attempt with a stored output).
// It backs the drive's wedge recovery: a newer failed or pending run under
// the same key does not erase the wave's already-produced chunks, whose
// content-addressed output stays durable. Mirrors
// stackDecomposeContinueRunRef but filters on loadable output instead of
// returning the newest run unconditionally.
func succeededDecomposeContinueRunRef(repo workflowledger.Repository, stackID string, wave int) (workflowledger.RunSnapshot, bool, error) {
	key, err := stackDecomposeContinueKey(stackID, wave)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if _, err := loadStackPlanOutput(repo, r.RunID); err != nil {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// admitNextWaveIfReady is runStackDrive's one-pass-per-invocation extension
// point for §12.1 incremental decompose: if everything currently known just
// merged and the latest decompose wave declared more scope, request exactly
// the next wave (a bounded extra step, not a full drive-to-completion loop -
// the operator re-runs `stack drive` to keep advancing, matching this
// command's existing one-pass-per-invocation contract).
func admitNextWaveIfReady(prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, chunks []ChunkPlan, hasMore bool, remainingScope string, planInputs map[string]string, stdout, stderr io.Writer) error {
	if !hasMore {
		return nil
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	if !allChunksMerged(chunks, stackMergedSet(byID)) {
		return nil
	}
	wave, err := latestDecomposeContinueWave(prepared.Repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	wave++
	// Halt BEFORE admitting when the cap is already reached (same rationale
	// as driveStackToCompletion's pre-admission check).
	if maxTotal := prepared.Compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks) >= maxTotal {
		return fmt.Errorf("stack drive: stack %s reached max_total_chunks=%d with more scope declared; halting before admitting wave %d", stackID, maxTotal, wave)
	}
	nextChunks, _, _, err := admitDecomposeContinuationRun(prepared, stackID, wave, remainingScope, planInputs, stdout, stderr)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if maxTotal := prepared.Compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks)+len(nextChunks) > maxTotal {
		return fmt.Errorf("stack drive: stack %s would admit %d total chunks, exceeding max_total_chunks=%d",
			stackID, len(chunks)+len(nextChunks), maxTotal)
	}
	if err := seedStackLedger(ledger, stackID, nextChunks); err != nil {
		return fmt.Errorf("stack drive: wave %d seed: %w", wave, err)
	}
	return nil
}

// admitNextDecomposeWave admits decompose-continuation wave N and seeds its
// chunks. It halts BEFORE admitting when the cap is already reached: an
// admitted continuation run whose chunks the cap then rejects is an orphan
// (admitted, never seeded, never driven). The post-admission check stays for
// a wave that alone jumps over the cap.
func admitNextDecomposeWave(prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, wave int, chunks []ChunkPlan, remainingScope string, planInputs map[string]string, stdout, stderr io.Writer) ([]ChunkPlan, bool, string, error) {
	if maxTotal := prepared.Compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks) >= maxTotal {
		return nil, false, "", fmt.Errorf("stack drive: stack %s reached max_total_chunks=%d with more scope declared; halting before admitting wave %d", stackID, maxTotal, wave)
	}
	nextChunks, nextHasMore, nextRemaining, err := admitDecomposeContinuationRun(prepared, stackID, wave, remainingScope, planInputs, stdout, stderr)
	if err != nil {
		return nil, false, "", fmt.Errorf("stack drive: %w", err)
	}
	if maxTotal := prepared.Compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks)+len(nextChunks) > maxTotal {
		return nil, false, "", fmt.Errorf("stack drive: stack %s would admit %d total chunks, exceeding max_total_chunks=%d (already have %d, wave %d adds %d)",
			stackID, len(chunks)+len(nextChunks), maxTotal, len(chunks), wave, len(nextChunks))
	}
	if err := seedStackLedger(ledger, stackID, nextChunks); err != nil {
		return nil, false, "", fmt.Errorf("stack drive: wave %d seed: %w", wave, err)
	}
	return nextChunks, nextHasMore, nextRemaining, nil
}
