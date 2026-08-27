package ledger

// Regression test for the coordinator-ledger claim fence (recovery audit
// finding 1): every mutation of a claimed run must be rejected for a holder
// that does not own the run. Without the fence, a stale executor whose claim
// was force-cleared by another host keeps writing task/output/event records,
// so the same run's work is recorded twice by two hosts. Mirrors the
// workflows-ledger TestIntegrationClaimFencesWorkflowMutation.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestStorageLedgerClaimFencesMutations(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "claimfence.db"))
	if err != nil {
		t.Fatal(err)
	}
	repoA := NewStorageLedgerRepository(store)
	repoB := NewStorageLedgerRepository(store)
	t.Cleanup(func() { _ = repoA.Close() })
	t.Cleanup(func() { _ = repoB.Close() })

	runID := "run-claim-fence"
	now := time.Now()
	if err := repoA.CreateRun(ctx, "key-1", RunSnapshot{
		RunID: runID, Status: RunStatusCreated, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	task := TaskSnapshot{RunID: runID, TaskID: "t1", HandlerName: "h", Input: []byte(`{}`), Scope: "test", Status: string(TaskStatusQueued)}
	if err := repoA.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask before claim: %v", err)
	}

	// repoA claims the run; its own mutations are fenced with holder-a and
	// succeed.
	if err := repoA.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("repoA claim: %v", err)
	}
	if err := repoA.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t2", HandlerName: "h", Input: []byte(`{}`), Scope: "test", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("repoA CreateTask under own claim: %v", err)
	}
	if err := repoA.AppendEvent(ctx, LifecycleEvent{ID: "evt-a", RunID: runID, Kind: "task_running", TaskID: "t1"}); err != nil {
		t.Fatalf("repoA AppendEvent under own claim: %v", err)
	}
	snap, err := repoA.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if err := repoA.CompareAndSetTaskStatus(ctx, runID, "t1", snap.Version, "running"); err != nil {
		t.Fatalf("repoA CAS under own claim: %v", err)
	}
	if err := repoA.SetTaskOutput(ctx, runID, "t1", "ref:out:x", "", ""); err != nil {
		t.Fatalf("repoA SetTaskOutput under own claim: %v", err)
	}

	// repoB does NOT hold the claim: every mutation must fail closed with
	// ErrClaimHeld, exactly as a stale executor's writes fail once another
	// host has taken the run.
	if err := repoB.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t3", HandlerName: "h", Input: []byte(`{}`), Scope: "test", Status: string(TaskStatusQueued)}); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB CreateTask on claimed run: err = %v, want ErrClaimHeld", err)
	}
	if err := repoB.AppendEvent(ctx, LifecycleEvent{ID: "evt-b", RunID: runID, Kind: "task_running", TaskID: "t1"}); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB AppendEvent on claimed run: err = %v, want ErrClaimHeld", err)
	}
	if err := repoB.CompareAndSetTaskStatus(ctx, runID, "t1", 1, "failed"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB CAS on claimed run: err = %v, want ErrClaimHeld", err)
	}
	if err := repoB.SetTaskOutput(ctx, runID, "t1", "ref:out:b", "", ""); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB SetTaskOutput on claimed run: err = %v, want ErrClaimHeld", err)
	}

	// ClearRunClaim drops repoA's in-memory holder too, so repoB can take
	// the run and repoA's subsequent writes are fenced against repoB's claim.
	if err := repoA.ClearRunClaim(ctx, runID); err != nil {
		t.Fatalf("repoA ClearRunClaim: %v", err)
	}
	if err := repoB.ClaimRun(ctx, runID, "holder-b"); err != nil {
		t.Fatalf("repoB claim after clear: %v", err)
	}
	if err := repoA.AppendEvent(ctx, LifecycleEvent{ID: "evt-a2", RunID: runID, Kind: "task_running", TaskID: "t1"}); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoA AppendEvent after losing claim: err = %v, want ErrClaimHeld", err)
	}
	// repoB, now the holder, writes succeed.
	if err := repoB.AppendEvent(ctx, LifecycleEvent{ID: "evt-b2", RunID: runID, Kind: "task_succeeded", TaskID: "t1"}); err != nil {
		t.Fatalf("repoB AppendEvent under own claim: %v", err)
	}
}
