package cliworkflow

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ---------------------------------------------------------------------------
// `mivia workflow delete <run_id>` command
// ---------------------------------------------------------------------------

// TestWorkflowDeleteCommandDeliveryPending deletes a delivery_pending run and
// asserts the ledger record is gone: GetRun is ErrNotFound, a second delete
// reports not found, and the pre-delete status is echoed to stdout.
func TestWorkflowDeleteCommandDeliveryPending(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)

	var stdout strings.Builder
	if err := RunWorkflowWithIO([]string{"delete", runID, "--workspace", root, "--config", config}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow delete error = %v", err)
	}
	if !strings.Contains(stdout.String(), "deleted=true") || !strings.Contains(stdout.String(), "delivery_pending") {
		t.Fatalf("delete output = %q, want deleted=true delivery_pending", stdout.String())
	}
	if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after delete = %v, want ErrNotFound", err)
	}
	if err := RunWorkflowWithIO([]string{"delete", runID, "--workspace", root, "--config", config}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("second delete error = %v, want not-found", err)
	}
}

// TestWorkflowDeleteCommandTerminalRun deletes a run settled to a terminal
// status (succeeded), the other deletable class.
func TestWorkflowDeleteCommandTerminalRun(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, run.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatalf("CAS to succeeded: %v", err)
	}

	var stdout strings.Builder
	if err := RunWorkflowWithIO([]string{"delete", runID, "--workspace", root, "--config", config}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow delete error = %v", err)
	}
	if !strings.Contains(stdout.String(), "succeeded") {
		t.Fatalf("delete output = %q, want succeeded", stdout.String())
	}
	if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after delete = %v, want ErrNotFound", err)
	}
}

// TestWorkflowDeleteCommandRefusesActiveRun pins the fail-closed gate: an
// active run is refused and left untouched.
func TestWorkflowDeleteCommandRefusesActiveRun(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	err := RunWorkflowWithIO([]string{"delete", runID, "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cancel it before delete") {
		t.Fatalf("workflow delete error = %v, want cancel-first refusal", err)
	}
	repo := openWorkflowTestStore(t, storePath)
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused delete: %v", err)
	}
}

// TestWorkflowDeleteCommandMissingRun pins not-found for an unknown run ID.
func TestWorkflowDeleteCommandMissingRun(t *testing.T) {
	root, configPath, _, _ := newGatedApprovalFixture(t)
	err := RunWorkflowWithIO([]string{"delete", "wfr-missing", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("workflow delete error = %v, want not-found", err)
	}
}

// TestWorkflowDeleteCommandLockSafety verifies `workflow delete` runs under
// the workflow execution file lock like the other mutating operator commands:
// a concurrent holder fails the command instead of racing the ledger claim.
func TestWorkflowDeleteCommandLockSafety(t *testing.T) {
	ShortenWorkflowResolutionLockWaitForTest(t)
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	release, err := AcquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = RunWorkflowWithIO([]string{"delete", runID, "--workspace", root, "--config", config}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("delete under a held execution lock succeeded; want a refusal")
	}
	repo := openWorkflowTestStore(t, storePath)
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a lock-refused delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// session engine (agent tool path)
// ---------------------------------------------------------------------------

// TestSessionEngineDeleteRemovesSettledRun exercises the agent-facing engine
// on a delivery_pending run: the ledger record is removed and the result
// carries the pre-delete status.
func TestSessionEngineDeleteRemovesSettledRun(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)

	e := NewSessionWorkflowEngine(root, config)
	result, err := e.Delete(context.Background(), runID, false)
	if err != nil {
		t.Fatalf("engine delete: %v", err)
	}
	if result.RunID != runID || !result.Deleted || result.Status != string(workflowledger.RunStatusDeliveryPending) {
		t.Fatalf("delete result = %+v", result)
	}
	if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after engine delete = %v, want ErrNotFound", err)
	}
}

// TestSessionEngineDeleteRefusesActiveRun pins the engine gate: an active run
// is refused and its claim is left untouched.
func TestSessionEngineDeleteRefusesActiveRun(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	e := NewSessionWorkflowEngine(root, configPath)
	_, err := e.Delete(context.Background(), runID, false)
	if err == nil || !strings.Contains(err.Error(), "cancel it before delete") {
		t.Fatalf("engine delete error = %v, want cancel-first refusal", err)
	}
	repo := openWorkflowTestStore(t, storePath)
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused engine delete: %v", err)
	}
}

// TestSessionEngineDeleteRefusesForeignClaim pins that a fresh foreign claim
// (a live delivery or another executor) refuses deletion: never blind-clear.
func TestSessionEngineDeleteRefusesForeignClaim(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)
	if err := repo.ClaimRun(context.Background(), runID, "foreign-delete-host"); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, config)
	_, err := e.Delete(context.Background(), runID, false)
	if err == nil || !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("engine delete error = %v, want foreign-claim refusal", err)
	}
	if err := repo.ClaimRun(context.Background(), runID, "probe"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after refused delete = %v, want still ErrClaimHeld", err)
	}
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused engine delete: %v", err)
	}
}

// TestWorkflowDeleteCommandForceActiveRun pins the crash-recovery override:
// --force permits deleting a non-terminal run (here: waiting_approval) that a
// dead executor left stranded, exactly what resume --force and deliver --force
// are for. Without force the same run is refused (see
// TestWorkflowDeleteCommandRefusesActiveRun).
func TestWorkflowDeleteCommandForceActiveRun(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	if err := RunWorkflowWithIO([]string{"delete", runID, "--force", "--workspace", root, "--config", configPath}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow delete --force error = %v", err)
	}
	if !strings.Contains(stdout.String(), "deleted=true") {
		t.Fatalf("delete output = %q, want deleted=true", stdout.String())
	}
	repo := openWorkflowTestStore(t, storePath)
	if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after force delete = %v, want ErrNotFound", err)
	}
}

// TestSessionEngineDeleteForceActiveRun pins the engine-level override: force
// deletes a non-terminal run the same way the CLI --force does.
func TestSessionEngineDeleteForceActiveRun(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	e := NewSessionWorkflowEngine(root, configPath)
	result, err := e.Delete(context.Background(), runID, true)
	if err != nil {
		t.Fatalf("engine delete with force: %v", err)
	}
	if result.RunID != runID || !result.Deleted {
		t.Fatalf("delete result = %+v, want deleted for %s", result, runID)
	}
	repo := openWorkflowTestStore(t, storePath)
	if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after force delete = %v, want ErrNotFound", err)
	}
}

// TestSessionEngineDeleteForceStillRefusesFreshClaim pins that force unlocks
// only the STATUS gate, never the claim fence: a fresh claim held by a live
// executor still refuses deletion, so an actively executing run can never be
// deleted even with force.
func TestSessionEngineDeleteForceStillRefusesFreshClaim(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	repo := openWorkflowTestStore(t, storePath)
	if err := repo.ClaimRun(context.Background(), runID, "foreign-delete-host"); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, configPath)
	_, err := e.Delete(context.Background(), runID, true)
	if err == nil || !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("engine delete with force = %v, want foreign-claim refusal", err)
	}
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused force delete: %v", err)
	}
}
