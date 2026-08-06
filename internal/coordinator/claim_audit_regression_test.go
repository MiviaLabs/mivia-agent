package coordinator_test

// Regression coverage promoted from the recovery audit's reproduction scratch
// (steerrepro): claim fencing interplay for cancelRecovered, reclaim/delete,
// and delete+recreate watermark convergence over one SQLite store.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func auditNewPool(t *testing.T) *subagents.Pool {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	return subagents.New(d, subagents.Policy{Workers: 1})
}

func auditCloseStore(t *testing.T, store *storage.SQLite) {
	t.Helper()
	t.Cleanup(func() { _ = store.Close() })
}

// TestCancelRecoveredRefusesForeignClaim pins the fail-closed claim contract
// for cancelRecovered: a run whose claim row is held by ANOTHER executor is
// refused outright. The claim is never cleared (clearing it would fence a
// possibly LIVE owner mid-flight - its fenced appends would start failing with
// ErrClaimHeld) and the run's tasks are left untouched.
func TestCancelRecoveredRefusesForeignClaim(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repoA := ledger.NewBorrowedStorageLedgerRepository(store)
	now := time.Now()
	if err := repoA.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := ledger.TaskSnapshot{
		RunID: "run-x", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-x", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}
	if err := repoA.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// A (holder-a) holds the execution claim - a live owner, not a crashed one.
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatal(err)
	}

	// B recovers the same keyed run; its Cancel must refuse the foreign claim.
	repoB := ledger.NewBorrowedStorageLedgerRepository(store)
	c2 := coordinator.New(repoB, auditNewPool(t))
	h, err := c2.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatalf("spawn dedup onto claimed run: %v", err)
	}
	if err := c2.Cancel(ctx, h); err == nil || !strings.Contains(err.Error(), "refusing to clear a possibly live claim") {
		t.Fatalf("cancelRecovered on a foreign claim: err = %v, want fail-closed refusal", err)
	}
	// The claim must still be held by holder-a: A's re-probe succeeds.
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatalf("A claim after B's refused cancel: %v (want nil - claim still held by A)", err)
	}
	// The queued task is untouched.
	taskSnap, err := repoA.GetTask(ctx, "run-x", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if taskSnap.Status != string(ledger.TaskStatusQueued) {
		t.Fatalf("task status after refused cancel = %q, want queued", taskSnap.Status)
	}
	_ = repoA.Close()
	_ = repoB.Close()
	auditCloseStore(t, store)
}

// TestCancelRecoveredRefusesLiveClaimOnRunningTask is the audit regression for
// the claim-fencing fix: coordinator A holds the claim on a run whose task is
// RUNNING (a live execution). B recovers the run via Spawn and tries to cancel
// it. B must refuse without touching the claim or the task, and A's fenced
// ledger operations (append/CAS) must keep working - they must NOT fail with
// ErrClaimHeld, which is exactly what the old clear-and-retake behaviour did
// to the live owner.
func TestCancelRecoveredRefusesLiveClaimOnRunningTask(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repoA := ledger.NewBorrowedStorageLedgerRepository(store)
	now := time.Now()
	if err := repoA.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := ledger.TaskSnapshot{
		RunID: "run-x", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusRunning), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-x", AttemptNum: 1, Status: string(ledger.TaskStatusRunning)}},
	}
	if err := repoA.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// A holds the execution claim on a live running execution.
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatal(err)
	}

	repoB := ledger.NewBorrowedStorageLedgerRepository(store)
	c2 := coordinator.New(repoB, auditNewPool(t))
	h, err := c2.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatalf("spawn dedup onto claimed run: %v", err)
	}
	if err := c2.Cancel(ctx, h); err == nil || !strings.Contains(err.Error(), "refusing to clear a possibly live claim") {
		t.Fatalf("cancelRecovered on a live claim: err = %v, want fail-closed refusal", err)
	}

	// The running task is untouched and A's claim survives B's refused cancel.
	taskSnap, err := repoA.GetTask(ctx, "run-x", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if taskSnap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("task status after refused cancel = %q, want running", taskSnap.Status)
	}
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatalf("A claim after B's refused cancel: %v (want nil - claim still held by A)", err)
	}
	// A's fenced ledger operations still succeed: a claim-fenced append and a
	// task CAS must NOT fail with ErrClaimHeld after B's cancel attempt (the
	// old clear-and-retake behaviour made both fail, fencing the live owner).
	evt := ledger.LifecycleEvent{ID: "audit-evt-1", RunID: "run-x", Kind: "task_running", TaskID: "t1", AttemptID: "att-1"}
	if err := repoA.AppendEvent(ctx, evt); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			t.Fatalf("A was fenced by B's cancel attempt: fenced append failed with ErrClaimHeld")
		}
		t.Fatalf("A fenced append after B's refused cancel: %v", err)
	}
	if err := repoA.CompareAndSetTaskStatus(ctx, "run-x", "t1", 1, string(ledger.TaskStatusCompleted)); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			t.Fatalf("A was fenced by B's cancel attempt: task CAS failed with ErrClaimHeld")
		}
		t.Fatalf("A task CAS after B's refused cancel: %v", err)
	}
	_ = repoA.Close()
	_ = repoB.Close()
	auditCloseStore(t, store)
}

// TestReclaimDeleteSucceedsInForceResumeClearWindow documents the residual
// bounded window in probe-then-clear force-resume: if the reclaimer's
// DeleteRun lands between B's ClearRunClaim and re-claim, the delete
// succeeds and B's resume fails cleanly (CreateTask on a deleted run) rather
// than corrupting state. The fence backstop (T2b) blocks the delete once B
// re-claims.
func TestReclaimDeleteSucceedsInForceResumeClearWindow(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed := ledger.NewBorrowedStorageLedgerRepository(store)
	old := time.Now().Add(-2 * time.Minute)
	if err := seed.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusCreated, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	repoA := ledger.NewBorrowedStorageLedgerRepository(store)
	repoB := ledger.NewBorrowedStorageLedgerRepository(store)

	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatal(err)
	}
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); !errors.Is(err, ledger.ErrClaimHeld) {
		t.Fatalf("B probe claim: %v", err)
	}
	if err := repoB.ClearRunClaim(ctx, "run-x"); err != nil {
		t.Fatal(err)
	}
	if err := repoA.DeleteRun(ctx, "run-x"); err != nil {
		t.Fatalf("A DeleteRun in the clear window failed: %v", err)
	}
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); err != nil {
		t.Fatalf("B reclaim: %v", err)
	}
	// B's resume then creates a task on the deleted run: the append passes
	// (fence holder B) but the projection has no run -> clean ErrNotFound.
	if err := repoB.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-x", TaskID: "t1", HandlerName: "worker", Status: string(ledger.TaskStatusQueued), Version: 1}); err == nil {
		t.Fatal("B CreateTask on a deleted run succeeded; want a clean failure")
	}
	_ = repoA.Close()
	_ = repoB.Close()
	auditCloseStore(t, store)
}

// TestReclaimDeleteBlockedAfterResumerReclaims pins the fence backstop: once
// the force-resumer has re-claimed, the reclaimer's DeleteRun is refused.
func TestReclaimDeleteBlockedAfterResumerReclaims(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed := ledger.NewBorrowedStorageLedgerRepository(store)
	old := time.Now().Add(-2 * time.Minute)
	if err := seed.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusCreated, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	repoA := ledger.NewBorrowedStorageLedgerRepository(store)
	repoB := ledger.NewBorrowedStorageLedgerRepository(store)
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatal(err)
	}
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); !errors.Is(err, ledger.ErrClaimHeld) {
		t.Fatalf("B probe claim: %v", err)
	}
	if err := repoB.ClearRunClaim(ctx, "run-x"); err != nil {
		t.Fatal(err)
	}
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); err != nil {
		t.Fatal(err)
	}
	err = repoA.DeleteRun(ctx, "run-x")
	if !errors.Is(err, ledger.ErrClaimHeld) {
		t.Fatalf("A DeleteRun after B reclaimed: err = %v, want ErrClaimHeld (fence backstop)", err)
	}
	_ = repoA.Close()
	_ = repoB.Close()
	auditCloseStore(t, store)
}

// TestDeleteRunWhileOwnClaimHeld pins releaseAndDeleteRun ordering: delete
// with the same instance's claim held succeeds.
func TestDeleteRunWhileOwnClaimHeld(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewBorrowedStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-y", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, "run-y", "holder-a"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "run-y"); err != nil {
		t.Fatalf("DeleteRun with own claim held: %v", err)
	}
	_ = repo.Close()
	auditCloseStore(t, store)
}

// TestRecoverWatermarkAfterDeleteRecreate pins Recover/cursor convergence
// after delete + recreate with the same idempotency key under fenced appends:
// a second reader and a fresh third instance must both see the recreated run.
func TestRecoverWatermarkAfterDeleteRecreate(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	writer := ledger.NewBorrowedStorageLedgerRepository(store)
	reader := ledger.NewBorrowedStorageLedgerRepository(store)
	if err := writer.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-z", Status: ledger.RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "run-z"); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteRun(ctx, "run-z"); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-z", Status: ledger.RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
	if err := writer.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-z", TaskID: "t1", HandlerName: "worker", Status: string(ledger.TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	snap, err := reader.GetRunByIdempotencyKey(ctx, "K")
	if err != nil {
		t.Fatalf("reader key lookup after delete+recreate: %v", err)
	}
	if snap.RunID != "run-z" {
		t.Fatalf("key resolves to %q", snap.RunID)
	}
	tasks, err := reader.ListTasks(ctx, "run-z")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reader tasks after recreate: %d err=%v", len(tasks), err)
	}
	fresh := ledger.NewBorrowedStorageLedgerRepository(store)
	rec, err := fresh.Recover(ctx)
	if err != nil {
		t.Fatalf("fresh Recover: %v", err)
	}
	if len(rec) != 1 || rec[0].RunID != "run-z" {
		t.Fatalf("fresh Recover sees %d runs: %+v", len(rec), rec)
	}
	_ = writer.Close()
	_ = reader.Close()
	_ = fresh.Close()
	auditCloseStore(t, store)
}

// TestCancelRecoveredWithoutClaimRow pins the normal recovered-cancel path
// (no claim row at all) still succeeding under the fence.
func TestCancelRecoveredWithoutClaimRow(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewBorrowedStorageLedgerRepository(store)
	now := time.Now()
	if err := repo.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-w", Status: ledger.RunStatusQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "run-w", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-w", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, auditNewPool(t))
	h, err := c.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(ctx, h); err != nil {
		t.Fatalf("cancelRecovered with no claim row failed: %v", err)
	}
	_ = repo.Close()
	auditCloseStore(t, store)
}
