package cli

// Fail-settle regression for the one-shot `mivia stack drive` command: when
// the drive pass hits a terminally failed chunk, the plan run must be settled
// to failed instead of staying parked at delivery_pending forever.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// TestStackDriveFailSettlesPlanRunFailed proves that driveStackOnePass
// fail-settles a delivery_pending plan run when its stack has a durably failed
// chunk. Before this fix the command returned the drive error and left the run
// parked; after the fix the run reaches the failed terminal.
func TestStackDriveFailSettlesPlanRunFailed(t *testing.T) {
	prepared, planRunID := seedStackDriveFailSettleFixture(t)
	err := driveStackOnePass(prepared, planRunID, map[string]string{"task": "x"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot complete") {
		t.Fatalf("driveStackOnePass() error = %v, want errFailedStackPlanRun", err)
	}
	run, err := prepared.repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed after fail-settle", run.Status)
	}
}

// seedStackDriveFailSettleFixture builds a delivery_pending plan run whose
// stack has one chunk that exhausted its retry budget and a failed run row.
func seedStackDriveFailSettleFixture(t *testing.T) (*preparedWorkflowRun, string) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"})
	if err != nil {
		t.Fatalf("prepareWorkflowRun() error = %v", err)
	}
	t.Cleanup(prepared.closeFn)

	const planRunID = "wfr-drive-fail-settle"
	seedPlanRunDeliveryPending(t, prepared.repo, planRunID, prepared.compiled.Digest)
	seedSucceededDecomposeAttempt(t, prepared.repo, planRunID, []byte(multiChunkPlanOutput))
	seedExhaustedFailedChunk(t, prepared, planRunID, "c1")
	return prepared, planRunID
}

// seedPlanRunDeliveryPending creates the plan run row and moves it to
// delivery_pending.
func seedPlanRunDeliveryPending(t *testing.T, repo workflowledger.Repository, planRunID, digest string) {
	t.Helper()
	ctx := context.Background()
	inputs := map[string]string{"task": "x"}
	rawSnap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	planRun := workflowledger.RunSnapshot{
		RunID: planRunID, WorkflowName: "mini-stack", WorkflowDigest: digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnap),
		InputDigest:    workflowledger.InputDigest(inputs),
		Status:         workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, planRun, rawSnap); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		stored, err := repo.GetRun(ctx, planRunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, planRunID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// seedExhaustedFailedChunk pre-seeds a chunk whose retry budget is exhausted
// (reopened stackMaxChunkAttempts times) with a failed run row: reconcile will
// mark it terminally failed, halting the drive pass.
func seedExhaustedFailedChunk(t *testing.T, prepared *preparedWorkflowRun, planRunID, failedChunkID string) {
	t.Helper()
	ctx := context.Background()
	ledger := tasks.NewStore(prepared.store)
	if _, err := ledger.StorePlan(tasks.Plan{ID: planRunID, Scope: stackScope(planRunID), Schema: stacking.PlanSchema}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(tasks.Task{ID: failedChunkID, PlanRef: planRunID, Scope: stackScope(planRunID), Status: stackStatusRunning}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stackMaxChunkAttempts; i++ {
		if err := ledger.TransitionTask(planRunID, failedChunkID, stackStatusReopened); err != nil {
			t.Fatal(err)
		}
	}
	key, err := stackAdmissionKey(planRunID, failedChunkID)
	if err != nil {
		t.Fatal(err)
	}
	chunkSnap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	chunkRun := workflowledger.RunSnapshot{
		RunID: "wfr-fail-c1", InvocationKey: key, WorkflowName: "mini-stack",
		Status: workflowledger.RunStatusPending,
	}
	if err := prepared.repo.CreateRun(ctx, chunkRun, chunkSnap); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusFailed} {
		stored, err := prepared.repo.GetRun(ctx, chunkRun.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.repo.CompareAndSetRunStatus(ctx, chunkRun.RunID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}
