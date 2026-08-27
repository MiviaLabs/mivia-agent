package localengine

import (
	"context"
	"errors"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newSettledEngine returns an engine over a fresh memory repo with one run
// settled to the given status via valid CAS edges (pending -> running -> to).
func newSettledEngine(t *testing.T, to workflowledger.RunStatus) (*Engine, workflowledger.Repository, string) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	engine := &Engine{Repo: repo}
	runID := "wfr-del-" + strings.ReplaceAll(t.Name(), "/", "-")
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "del-test", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), snap, raw); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, 2, to, nil); err != nil {
		t.Fatal(err)
	}
	return engine, repo, runID
}

// TestEngineDeleteSettledRun deletes delivery_pending and terminal runs and
// asserts the ledger record is gone.
func TestEngineDeleteSettledRun(t *testing.T) {
	for _, to := range []workflowledger.RunStatus{
		workflowledger.RunStatusDeliveryPending,
		workflowledger.RunStatusSucceeded,
		workflowledger.RunStatusFailed,
		workflowledger.RunStatusCanceled,
	} {
		t.Run(string(to), func(t *testing.T) {
			engine, repo, runID := newSettledEngine(t, to)
			result, err := engine.Delete(context.Background(), runID, false)
			if err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if result.RunID != runID || !result.Deleted || result.Status != string(to) {
				t.Fatalf("result = %+v, want deleted run_id=%s status=%s", result, runID, to)
			}
			if _, err := repo.GetRun(context.Background(), runID); !errors.Is(err, workflowledger.ErrNotFound) {
				t.Fatalf("GetRun after delete = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestEngineDeleteForgetsWorktree pins that Delete removes the run's entry
// from e.worktrees, not just the ledger record. Before this fix nothing ever
// called forgetWorktree/delete on that map, so a long-lived engine process
// leaked one entry per run forever.
func TestEngineDeleteForgetsWorktree(t *testing.T) {
	engine, _, runID := newSettledEngine(t, workflowledger.RunStatusSucceeded)
	engine.recordWorktree(runID, Identity{Root: "/tmp/wt", WorktreeName: "workflow-" + runID})
	if _, ok := engine.worktreeIdentity(runID); !ok {
		t.Fatal("recordWorktree did not record the identity")
	}
	if _, err := engine.Delete(context.Background(), runID, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := engine.worktreeIdentity(runID); ok {
		t.Fatal("Delete left the run's worktree identity behind; e.worktrees leaks")
	}
}

// TestEngineDeleteRefusesActiveStatus pins the status gate: an active run is
// refused and left untouched.
func TestEngineDeleteRefusesActiveStatus(t *testing.T) {
	engine, repo, _ := newSettledEngine(t, workflowledger.RunStatusDeliveryPending)
	// running is not reachable from delivery_pending without a controller;
	// create a fresh run that stays running instead.
	run2 := "wfr-del-running-" + t.Name()
	snap := workflowledger.RunSnapshot{RunID: run2, WorkflowName: "del-test", Status: workflowledger.RunStatusPending, ActiveStepID: "start"}
	if err := repo.CreateRun(context.Background(), snap, []byte(`{"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run2, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Delete(context.Background(), run2, false)
	if err == nil || !strings.Contains(err.Error(), "cancel it before delete") {
		t.Fatalf("Delete of running = %v, want cancel-first refusal", err)
	}
	if _, err := repo.GetRun(context.Background(), run2); err != nil {
		t.Fatalf("running run must survive a refused delete: %v", err)
	}
}

// TestEngineDeleteMissingRun propagates ErrNotFound.
func TestEngineDeleteMissingRun(t *testing.T) {
	engine, _, _ := newSettledEngine(t, workflowledger.RunStatusSucceeded)
	_, err := engine.Delete(context.Background(), "wfr-no-such-run", false)
	if !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("Delete missing = %v, want ErrNotFound", err)
	}
}

// TestEngineDeleteRefusesInProcessDelivery pins that a delivery mid-publish
// in THIS engine refuses deletion (never strip the exclusion fence).
func TestEngineDeleteRefusesInProcessDelivery(t *testing.T) {
	engine, repo, runID := newSettledEngine(t, workflowledger.RunStatusDeliveryPending)
	engine.mu.Lock()
	engine.delivering = map[string]string{runID: "delivery-host"}
	engine.mu.Unlock()
	_, err := engine.Delete(context.Background(), runID, false)
	if err == nil || !strings.Contains(err.Error(), "being delivered") {
		t.Fatalf("Delete during in-process delivery = %v, want delivery refusal", err)
	}
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused delete: %v", err)
	}
}

// TestEngineDeleteRefusesInProcessActive pins that an in-process controller
// run refuses deletion even if its status gate would pass (defense in depth).
func TestEngineDeleteRefusesInProcessActive(t *testing.T) {
	engine, repo, runID := newSettledEngine(t, workflowledger.RunStatusSucceeded)
	engine.mu.Lock()
	if engine.active == nil {
		engine.active = make(map[string]*activeRun)
	}
	engine.active[runID] = &activeRun{done: make(chan struct{})}
	engine.mu.Unlock()
	_, err := engine.Delete(context.Background(), runID, false)
	if err == nil || !strings.Contains(err.Error(), "running in this engine") {
		t.Fatalf("Delete of in-process active run = %v, want in-engine refusal", err)
	}
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused delete: %v", err)
	}
}

// TestEngineDeleteRefusesFreshForeignClaim pins that a fresh foreign claim
// (a live delivery on another host) refuses deletion: never blind-clear.
func TestEngineDeleteRefusesFreshForeignClaim(t *testing.T) {
	engine, repo, runID := newSettledEngine(t, workflowledger.RunStatusDeliveryPending)
	if err := repo.ClaimRun(context.Background(), runID, "foreign-host"); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Delete(context.Background(), runID, false)
	if err == nil || !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("Delete with foreign claim = %v, want foreign-claim refusal", err)
	}
	if err := repo.ClaimRun(context.Background(), runID, "probe"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after refused delete = %v, want still ErrClaimHeld", err)
	}
	if _, err := repo.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run must survive a refused delete: %v", err)
	}
}

// TestEngineDeleteForceActiveStatus pins the crash-recovery override: force
// deletes a non-terminal (running) run stranded by a dead executor.
func TestEngineDeleteForceActiveStatus(t *testing.T) {
	engine, repo, _ := newSettledEngine(t, workflowledger.RunStatusDeliveryPending)
	run2 := "wfr-del-force-running-" + t.Name()
	snap := workflowledger.RunSnapshot{RunID: run2, WorkflowName: "del-test", Status: workflowledger.RunStatusPending, ActiveStepID: "start"}
	if err := repo.CreateRun(context.Background(), snap, []byte(`{"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run2, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Delete(context.Background(), run2, true)
	if err != nil {
		t.Fatalf("Delete with force: %v", err)
	}
	if result.RunID != run2 || !result.Deleted {
		t.Fatalf("result = %+v, want deleted run_id=%s", result, run2)
	}
	if _, err := repo.GetRun(context.Background(), run2); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after force delete = %v, want ErrNotFound", err)
	}
}

// TestEngineDeleteForceStillRefusesFreshForeignClaim pins that force unlocks
// only the STATUS gate, never the claim fence: a fresh foreign claim (a live
// executor on another host) refuses deletion even with force.
func TestEngineDeleteForceStillRefusesFreshForeignClaim(t *testing.T) {
	engine, repo, _ := newSettledEngine(t, workflowledger.RunStatusDeliveryPending)
	run2 := "wfr-del-force-claim-" + t.Name()
	snap := workflowledger.RunSnapshot{RunID: run2, WorkflowName: "del-test", Status: workflowledger.RunStatusPending, ActiveStepID: "start"}
	if err := repo.CreateRun(context.Background(), snap, []byte(`{"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run2, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(context.Background(), run2, "foreign-host"); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Delete(context.Background(), run2, true)
	if err == nil || !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("Delete with force + foreign claim = %v, want foreign-claim refusal", err)
	}
	if _, err := repo.GetRun(context.Background(), run2); err != nil {
		t.Fatalf("run must survive a refused force delete: %v", err)
	}
}
