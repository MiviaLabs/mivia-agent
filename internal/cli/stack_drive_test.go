package cli

// Fail-settle regression for the one-shot `mivia stack drive` command: when
// the drive pass hits a terminally failed chunk, the plan run must be settled
// to failed instead of staying parked at delivery_pending forever.

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
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
	run, err := prepared.Repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed after fail-settle", run.Status)
	}
}

// seedStackDriveFailSettleFixture builds a delivery_pending plan run whose
// stack has one chunk that exhausted its retry budget and a failed run row.
func seedStackDriveFailSettleFixture(t *testing.T) (*cliworkflow.PreparedWorkflowRun, string) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := cliworkflow.PrepareWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"})
	if err != nil {
		t.Fatalf("cliworkflow.PrepareWorkflowRun() error = %v", err)
	}
	t.Cleanup(prepared.CloseFn)

	const planRunID = "wfr-drive-fail-settle"
	seedPlanRunDeliveryPending(t, prepared.Repo, planRunID, prepared.Compiled.Digest)
	seedSucceededDecomposeAttempt(t, prepared.Repo, planRunID, []byte(multiChunkPlanOutput))
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

// seedStackDriveGateFixtureBase builds a delivery_pending mini-stack plan run
// with its two decomposed chunks seeded into the ledger (both still
// "planned"), so callers can layer either a terminal failure (for the Failed
// gate) or leave it as-is (for the Incomplete gate) on top.
func seedStackDriveGateFixtureBase(t *testing.T, planRunID string) *cliworkflow.PreparedWorkflowRun {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := cliworkflow.PrepareWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"})
	if err != nil {
		t.Fatalf("cliworkflow.PrepareWorkflowRun() error = %v", err)
	}
	t.Cleanup(prepared.CloseFn)

	seedPlanRunDeliveryPending(t, prepared.Repo, planRunID, prepared.Compiled.Digest)
	seedSucceededDecomposeAttempt(t, prepared.Repo, planRunID, []byte(multiChunkPlanOutput))

	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil {
		t.Fatal(err)
	}
	ledger := workflowledger.NewStore(prepared.Store)
	if err := seedStackLedger(ledger, planRunID, chunks); err != nil {
		t.Fatal(err)
	}
	return prepared
}

// seedStackDriveFailedGateFixture builds a delivery_pending plan run whose
// stack has one chunk directly marked failed (no need to exhaust the retry
// budget): enough for classifyStackPlanRunDelivery to report stackPlanRunFailed
// so callers that switch on the gate (settleStackPlanRunIfComplete,
// driveStackOnePass's settle-on-error path, SettleFailedStackPlanRunIfNeeded)
// can be tested directly.
func seedStackDriveFailedGateFixture(t *testing.T) (*cliworkflow.PreparedWorkflowRun, string) {
	t.Helper()
	const planRunID = "wfr-drive-failed-gate"
	prepared := seedStackDriveGateFixtureBase(t, planRunID)
	ledger := workflowledger.NewStore(prepared.Store)
	if err := ledger.TransitionTask(planRunID, "c2", stackStatusFailed); err != nil {
		t.Fatal(err)
	}
	return prepared, planRunID
}

// seedStackDriveIncompleteGateFixture builds a delivery_pending plan run whose
// stack has not driven at all (both chunks still "planned"): classifyStack-
// PlanRunDelivery reports stackPlanRunIncomplete, not stackPlanRunFailed, so
// callers that gate on Failed specifically (SettleFailedStackPlanRunIfNeeded)
// can be proven to no-op on it.
func seedStackDriveIncompleteGateFixture(t *testing.T) (*cliworkflow.PreparedWorkflowRun, string) {
	t.Helper()
	const planRunID = "wfr-drive-incomplete-gate"
	return seedStackDriveGateFixtureBase(t, planRunID), planRunID
}

// seedExhaustedFailedChunk pre-seeds a chunk whose retry budget is exhausted
// (reopened stackMaxChunkAttempts times) with a failed run row: reconcile will
// mark it terminally failed, halting the drive pass.
func seedExhaustedFailedChunk(t *testing.T, prepared *cliworkflow.PreparedWorkflowRun, planRunID, failedChunkID string) {
	t.Helper()
	ctx := context.Background()
	ledger := workflowledger.NewStore(prepared.Store)
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: planRunID, Scope: stackScope(planRunID), Schema: delivery.PlanSchema}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{ID: failedChunkID, PlanRef: planRunID, Scope: stackScope(planRunID), Status: stackStatusRunning}); err != nil {
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
	if err := prepared.Repo.CreateRun(ctx, chunkRun, chunkSnap); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusFailed} {
		stored, err := prepared.Repo.GetRun(ctx, chunkRun.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Repo.CompareAndSetRunStatus(ctx, chunkRun.RunID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSettleStackPlanRunIfCompleteFailedGateRefusesAndSettles pins the
// stackPlanRunFailed case of settleStackPlanRunIfComplete's switch: a stack
// whose gate already reads Failed must be refused (not silently treated as
// the routine "nothing to settle" outcome) and the plan run must land at the
// failed terminal.
func TestSettleStackPlanRunIfCompleteFailedGateRefusesAndSettles(t *testing.T) {
	prepared, planRunID := seedStackDriveFailedGateFixture(t)

	err := settleStackPlanRunIfComplete(context.Background(), prepared, planRunID, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot complete") {
		t.Fatalf("settleStackPlanRunIfComplete() error = %v, want errFailedStackPlanRun", err)
	}
	run, getErr := prepared.Repo.GetRun(context.Background(), planRunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed", run.Status)
	}
}

// TestDriveStackOnePassSettleFailurePropagates pins driveStackOnePass's
// settleErr != nil branch: when driveStack fails AND the fail-settle attempt
// itself errors (e.g. a transient store fault), the settle error must
// surface wrapped, and the plan run must be left exactly as it was
// (delivery_pending) rather than silently advancing.
func TestDriveStackOnePassSettleFailurePropagates(t *testing.T) {
	prepared, planRunID := seedStackDriveFailSettleFixture(t)
	prepared.Repo = &failingCASRepository{Repository: prepared.Repo, failStatus: workflowledger.RunStatusFailed}

	err := driveStackOnePass(prepared, planRunID, map[string]string{"task": "x"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "settle failed plan run") {
		t.Fatalf("driveStackOnePass() error = %v, want it to surface the settle failure", err)
	}
	run, getErr := prepared.Repo.GetRun(context.Background(), planRunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (settle failed, must not silently advance)", run.Status)
	}
}

// TestSettleStackPlanRunIfCompleteUnknownGateFailsClosed pins the fail-closed
// default case: a gate value outside the 4 declared stackPlanRunGate
// constants must refuse with an error naming the unknown value, never fall
// through as if the drive had ended cleanly. classifyStackPlanRunDelivery
// itself can never produce a 5th value; the seam
// classifyStackPlanRunDeliveryFn lets this test simulate one anyway, so a
// future gate value added without updating this switch is caught here
// instead of by production fail-open behavior.
func TestSettleStackPlanRunIfCompleteUnknownGateFailsClosed(t *testing.T) {
	prepared, planRunID := seedStackDriveIncompleteGateFixture(t)
	orig := classifyStackPlanRunDeliveryFn
	classifyStackPlanRunDeliveryFn = func(context.Context, string, *storage.SQLite, workflowledger.Repository, string, bool) stackPlanRunGate {
		return stackPlanRunGate(99)
	}
	defer func() { classifyStackPlanRunDeliveryFn = orig }()

	err := settleStackPlanRunIfComplete(context.Background(), prepared, planRunID, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown plan run classification") {
		t.Fatalf("settleStackPlanRunIfComplete() error = %v, want the unknown-classification refusal", err)
	}
}
