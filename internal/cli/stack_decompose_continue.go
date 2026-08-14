package cli

// Incremental, wave-scoped decompose (§12.1 of
// docs/architecture/spec-auto-split-oversized-prs.md): the invocation-key
// derivation and run-ledger lookups a decompose-continuation wave needs.
// Split out of stack_reconcile.go to keep that file under the repo's
// per-file line ceiling (.mivia/policy/go-structure.json).

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
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

// admitNextWaveIfReady is runStackDrive's one-pass-per-invocation extension
// point for §12.1 incremental decompose: if everything currently known just
// merged and the latest decompose wave declared more scope, request exactly
// the next wave (a bounded extra step, not a full drive-to-completion loop -
// the operator re-runs `stack drive` to keep advancing, matching this
// command's existing one-pass-per-invocation contract).
func admitNextWaveIfReady(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, hasMore bool, remainingScope string, planInputs map[string]string, stdout, stderr io.Writer) error {
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
	wave, err := latestDecomposeContinueWave(prepared.repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	wave++
	nextChunks, _, _, err := admitDecomposeContinuationRun(prepared, stackID, wave, remainingScope, planInputs, stdout, stderr)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if maxTotal := prepared.compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks)+len(nextChunks) > maxTotal {
		return fmt.Errorf("stack drive: stack %s would admit %d total chunks, exceeding max_total_chunks=%d",
			stackID, len(chunks)+len(nextChunks), maxTotal)
	}
	if err := seedStackLedger(ledger, stackID, nextChunks); err != nil {
		return fmt.Errorf("stack drive: wave %d seed: %w", wave, err)
	}
	return nil
}
