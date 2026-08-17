package cli

// Coverage for stackDriveCompleted's fail-closed default branch
// (workflow_tool_engine_reconcile.go:494-497): every RunStatus constant is
// already handled by one of the switch's explicit cases (pending/running/
// waiting_approval, delivery_pending, succeeded, and the four terminal
// failure statuses), so the default case can only be reached by a status
// value outside that enum. That never happens through the real state
// machine (ValidRunTransition only ever produces the nine defined
// constants), so a repository stub is used to inject one directly - the same
// "unrecognized status" shape a future added RunStatus constant, or a
// corrupted row, would produce.

import (
	"context"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// unknownIntegrationStatusRepository embeds a real Repository and rewrites
// the stack's integration run status to an unrecognized value in ListRuns,
// the lookup stackRunRef uses to resolve the integration run.
type unknownIntegrationStatusRepository struct {
	workflowledger.Repository
	stackID string
}

func (u unknownIntegrationStatusRepository) ListRuns(ctx context.Context, status ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
	runs, err := u.Repository.ListRuns(ctx, status...)
	if err != nil {
		return nil, err
	}
	key, kerr := stackAdmissionKey(u.stackID, stackIntegrationChunkID)
	if kerr != nil {
		return runs, nil
	}
	out := make([]workflowledger.RunSnapshot, 0, len(runs))
	for _, r := range runs {
		if r.InvocationKey == key {
			r.Status = workflowledger.RunStatus("archived")
		}
		out = append(out, r)
	}
	return out, nil
}

// TestStackDriveCompletedUnknownIntegrationStatusFailsClosed pins that an
// integration run settled at a status stackDriveCompleted's switch does not
// recognize is treated as NOT complete (fail closed), not silently reported
// complete.
func TestStackDriveCompletedUnknownIntegrationStatusFailsClosed(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	runID := seedParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, runID)
	seedStackIntegrationRun(t, repo, runID, workflowledger.RunStatusSucceeded)

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wrapped := unknownIntegrationStatusRepository{Repository: repo, stackID: runID}
	if got := stackDriveCompleted(context.Background(), root, store, wrapped, runID, "auto", true); got {
		t.Fatal("stackDriveCompleted = true, want false (fail closed) for an unrecognized integration run status")
	}
}
