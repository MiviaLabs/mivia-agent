package steerrepro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func newPool(t *testing.T) *subagents.Pool {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	return subagents.New(d, subagents.Policy{Workers: 1})
}

// T1: cancelRecovered on a SQLite-backed run whose claim was left by a crashed
// holder. Question (c): the CAS is fenced with an EMPTY holder (recovered
// handle never claimed), and the store still has the stale claim row -> CAS
// fails with ErrClaimHeld.
func TestCancelRecoveredWithStaleClaim(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo1 := ledger.NewStorageLedgerRepository(store)
	now := time.Now()
	if err := repo1.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := ledger.TaskSnapshot{
		RunID: "run-x", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-x", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}
	if err := repo1.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// Crash: the holder's claim is never released.
	if err := store.ClaimRun(ctx, "run-x", "stale-holder"); err != nil {
		t.Fatal(err)
	}
	if err := repo1.Close(); err != nil {
		t.Fatal(err)
	}

	repo2 := ledger.NewStorageLedgerRepository(store)
	c2 := coordinator.New(repo2, newPool(t)).(*coordinator.Coordinator)
	h, err := c2.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatalf("spawn dedup onto interrupted run: %v", err)
	}
	cancelErr := c2.Cancel(ctx, h)
	if cancelErr == nil {
		t.Fatalf("cancel succeeded on a run with a stale claim (pre-fence behavior); want the fence to refuse")
	}
	t.Logf("cancelRecovered error: %v", cancelErr)
	// Consequence: the queued task must remain queued (cancel refused).
	snap, err := c2.Inspect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run status after refused cancel: %s task=%s", snap.Status, snap.Tasks[0].Status)
	_ = repo2.Close()
}

// T1b: same flow over the fence-less MemoryLedgerRepository, showing the
// cancel succeeded before the claim fence was added.
func TestCancelRecoveredWithStaleClaimMemory(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	now := time.Now()
	if err := repo.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := ledger.TaskSnapshot{
		RunID: "run-x", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-x", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, "run-x", "stale-holder"); err != nil {
		t.Fatal(err)
	}
	c2 := coordinator.New(repo, newPool(t)).(*coordinator.Coordinator)
	h, err := c2.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatalf("spawn dedup: %v", err)
	}
	if err := c2.Cancel(ctx, h); err != nil {
		t.Fatalf("memory (unfenced) cancel failed: %v", err)
	}
	t.Log("memory backend: cancel succeeded (pre-fence semantics)")
}

// T2: question (b) force-resume vs reclaim. Window A: probe claim held by
// reclaimer; force-resumer clears it; reclaimer's DeleteRun lands while the
// claim row is empty -> delete SUCCEEDS against the run the resumer is about
// to claim.
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

	repoA := ledger.NewStorageLedgerRepository(store)
	repoB := ledger.NewStorageLedgerRepository(store)

	// Reclaimer A holds the probe claim.
	if err := repoA.ClaimRun(ctx, "run-x", "holder-a"); err != nil {
		t.Fatal(err)
	}
	// Force-resumer B: ClaimRun -> ErrClaimHeld -> ClearRunClaim.
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); !errors.Is(err, ledger.ErrClaimHeld) {
		t.Fatalf("B probe claim: %v", err)
	}
	if err := repoB.ClearRunClaim(ctx, "run-x"); err != nil {
		t.Fatal(err)
	}
	// A's DeleteRun now: the claim row is empty, so the fenced tombstone
	// append (holder A) PASSES and the run is deleted.
	if err := repoA.DeleteRun(ctx, "run-x"); err != nil {
		t.Fatalf("A DeleteRun in the clear window failed: %v (fence blocked it)", err)
	}
	t.Log("A deleted the run while B's force-resume had cleared the claim but not yet re-claimed")
	// B re-claims: succeeds, on a DELETED run.
	if err := repoB.ClaimRun(ctx, "run-x", "holder-b"); err != nil {
		t.Fatalf("B reclaim: %v", err)
	}
	// B's resume then creates a task on the deleted run: store append passes
	// (fence holder B), projection has no run -> CreateTask fails ErrNotFound.
	if err := repoB.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-x", TaskID: "t1", HandlerName: "worker", Status: string(ledger.TaskStatusQueued), Version: 1}); err != nil {
		t.Logf("B createTasks on deleted run failed: %v", err)
	}
	_ = repoA.Close()
	_ = repoB.Close()
}

// T2b: the fence backstop - if B has ALREADY re-claimed, A's DeleteRun fails.
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
	repoA := ledger.NewStorageLedgerRepository(store)
	repoB := ledger.NewStorageLedgerRepository(store)
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
	t.Log("fence backstop held: A could not delete the run B just claimed")
	_ = repoA.Close()
	_ = repoB.Close()
}

// T3: question (a) - releaseAndDeleteRun's delete succeeds while the claim is
// held, and the reclaim probe claim covers the delete.
func TestDeleteRunWhileClaimHeld(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-y", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, "run-y", "holder-a"); err != nil {
		t.Fatal(err)
	}
	// DeleteRun while the same instance holds the claim (releaseAndDeleteRun
	// order: delete first, claim held).
	if err := repo.DeleteRun(ctx, "run-y"); err != nil {
		t.Fatalf("DeleteRun with own claim held: %v", err)
	}
	t.Log("DeleteRun with own claim held: ok")
	_ = repo.Close()
}

// T4: question (d) - Recover/cursor drift after DeleteRun tombstones with the
// fenced appends: delete + recreate with the same idempotency key, converge in
// a second reader and a fresh third reader, then mutate the recreated run and
// confirm the second reader sees it.
func TestRecoverWatermarkAfterDeleteRecreate(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	writer := ledger.NewStorageLedgerRepository(store)
	reader := ledger.NewStorageLedgerRepository(store)
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
	// Reader must converge on the RECREATED run and its task.
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
	// A fresh third instance must converge too.
	fresh := ledger.NewStorageLedgerRepository(store)
	rec, err := fresh.Recover(ctx)
	if err != nil {
		t.Fatalf("fresh Recover: %v", err)
	}
	if len(rec) != 1 || rec[0].RunID != "run-z" {
		t.Fatalf("fresh Recover sees %d runs: %+v", len(rec), rec)
	}
	t.Log("delete+recreate watermark convergence: ok")
	_ = writer.Close()
	_ = reader.Close()
	_ = fresh.Close()
}

// T5: cancelRecovered CAS on a recovered run with NO claim row (normal
// dedup-on-completed/interrupted-unclaimed case) still passes (question c).
func TestCancelRecoveredWithoutClaimRow(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewStorageLedgerRepository(store)
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
	c := coordinator.New(repo, newPool(t)).(*coordinator.Coordinator)
	h, err := c.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(ctx, h); err != nil {
		t.Fatalf("cancelRecovered with no claim row failed: %v", err)
	}
	t.Log("cancelRecovered with no claim row: ok")
	_ = repo.Close()
}
