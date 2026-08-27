package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ---------------------------------------------------------------------------
// Crash-recovery tests (Phase B)
// ---------------------------------------------------------------------------

func TestStorageLedger_CrashRecovery_DetectsInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-crash", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	// Task 1: queued -> running -> completed
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t1", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t1", 2, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	// Task 2: queued -> running (interrupted mid-execution)
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t2", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t2", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	// Task 3: queued (never started)
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t3", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	// Close repo1 (simulates crash)
	if err := repo1.Close(); err != nil {
		t.Fatal(err)
	}

	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Should have found the interrupted run.
	found := false
	for _, r := range recovered {
		if r.RunID == "run-crash" {
			found = true
			if !r.WasInterrupted {
				t.Fatal("MUTATION FAIL: run with running/queued tasks should be marked interrupted")
			}
			break
		}
	}
	if !found {
		t.Fatal("recovered run not found")
	}

	// Verify task states post-recovery.
	tasks, err := repo2.ListTasks(ctx, "run-crash")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	// t1 should be completed, t2 running, t3 queued
	stateMap := make(map[string]string)
	for _, t := range tasks {
		stateMap[t.TaskID] = t.Status
	}
	if stateMap["t1"] != string(TaskStatusCompleted) {
		t.Fatalf("t1 status = %q, want %q", stateMap["t1"], TaskStatusCompleted)
	}
	if stateMap["t2"] != string(TaskStatusRunning) {
		t.Fatalf("t2 status = %q, want %q", stateMap["t2"], TaskStatusRunning)
	}
	if stateMap["t3"] != string(TaskStatusQueued) {
		t.Fatalf("t3 status = %q, want %q", stateMap["t3"], TaskStatusQueued)
	}
}

func TestStorageLedger_CrashRecovery_CompletedRunsNotInterrupted(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-done", Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	repo1.Close()

	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recovered {
		if r.RunID == "run-done" && r.WasInterrupted {
			t.Fatal("completed run should not be marked interrupted")
		}
	}
}

func TestStorageLedger_CrashRecovery_MultipleRuns(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })

	// Create two completed runs and one interrupted run.
	for i := 1; i <= 2; i++ {
		runID := fmt.Sprintf("run-completed-%d", i)
		if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCompleted}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-interrupted", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	repo1.Close()

	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 3 {
		t.Fatalf("expected 3 recovered runs, got %d", len(recovered))
	}
	interruptedCount := 0
	for _, r := range recovered {
		if r.WasInterrupted {
			interruptedCount++
		}
	}
	if interruptedCount != 1 {
		t.Fatalf("expected exactly 1 interrupted run, got %d", interruptedCount)
	}
}

// ---------------------------------------------------------------------------
// Storage oracle equivalence - formal proof that Memory and Storage backends
// produce identical projections under identical event sequences.
// ---------------------------------------------------------------------------

// applyEventSequence applies a deterministic sequence of operations to a
// LedgerRepository and returns the resulting run snapshots and events.
func applyEventSequence(repo LedgerRepository, now time.Time) error {
	ctx := context.Background()
	// Phase 1: Create run and tasks
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		return err
	}
	for i := 1; i <= 3; i++ {
		tid := fmt.Sprintf("t%d", i)
		if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: tid, Status: string(TaskStatusQueued), Version: 1, Attempts: []AttemptSnapshot{{AttemptID: "attempt-" + tid, TaskID: tid, RunID: "run-1", AttemptNum: 1, Status: string(TaskStatusQueued)}}}); err != nil {
			return err
		}
	}
	// Phase 2: Transition tasks with CAS version checks
	for i := 1; i <= 3; i++ {
		tid := fmt.Sprintf("t%d", i)
		// queued -> running
		if err := repo.CompareAndSetTaskStatus(ctx, "run-1", tid, 1, string(TaskStatusRunning)); err != nil {
			return err
		}
		// running -> completed (v2)
		if err := repo.CompareAndSetTaskStatus(ctx, "run-1", tid, 2, string(TaskStatusCompleted)); err != nil {
			return err
		}
		// Set output
		if err := repo.SetTaskOutput(ctx, "run-1", tid, "ref:output:10", "", ""); err != nil {
			return err
		}
		// Record attempt
		finished := now
		if err := repo.SetTaskAttempt(ctx, "run-1", tid, "attempt-"+tid, string(TaskStatusCompleted), &finished); err != nil {
			return err
		}
	}
	// Phase 3: Append lifecycle events
	events := []LifecycleEvent{
		{ID: "e1", RunID: "run-1", Kind: "task_created", TaskID: "t1"},
		{ID: "e2", RunID: "run-1", Kind: "task_running", TaskID: "t1"},
		{ID: "e3", RunID: "run-1", Kind: "task_completed", TaskID: "t1"},
		{ID: "e4", RunID: "run-1", Kind: "task_created", TaskID: "t2"},
		{ID: "e5", RunID: "run-1", Kind: "task_running", TaskID: "t2"},
		{ID: "e6", RunID: "run-1", Kind: "task_completed", TaskID: "t2"},
		{ID: "e7", RunID: "run-1", Kind: "task_created", TaskID: "t3"},
		{ID: "e8", RunID: "run-1", Kind: "task_running", TaskID: "t3"},
		{ID: "e9", RunID: "run-1", Kind: "task_completed", TaskID: "t3"},
	}
	for _, evt := range events {
		if err := repo.AppendEvent(ctx, evt); err != nil {
			return err
		}
	}
	// Phase 4: Attempt a stale version CAS (should fail with ErrConflict)
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusFailed)); err != ErrConflict {
		return fmt.Errorf("expected ErrConflict for stale version, got %v", err)
	}
	// Phase 5: Close the run
	if err := repo.CloseRun(ctx, "run-1"); err != nil {
		return err
	}
	return nil
}

// snapshotsEqual compares two RunSnapshot values for equality (excluding
// CompletedAt which may differ by nanosecond precision).
func snapshotsEqual(a, b RunSnapshot) bool {
	if a.RunID != b.RunID || a.DisplayName != b.DisplayName || a.Status != b.Status {
		return false
	}
	// Build task maps keyed by TaskID for order-independent comparison.
	aTasks := make(map[string]TaskSnapshot, len(a.Tasks))
	for _, t := range a.Tasks {
		aTasks[t.TaskID] = t
	}
	if len(a.Tasks) != len(b.Tasks) {
		return false
	}
	for _, tb := range b.Tasks {
		ta, ok := aTasks[tb.TaskID]
		if !ok {
			return false
		}
		if ta.Status != tb.Status || ta.Version != tb.Version {
			return false
		}
		if ta.OutputRef != tb.OutputRef || ta.ErrorRef != tb.ErrorRef {
			return false
		}
		if len(ta.DependsOn) != len(tb.DependsOn) {
			return false
		}
		for j := range ta.DependsOn {
			if ta.DependsOn[j] != tb.DependsOn[j] {
				return false
			}
		}
	}
	return true
}

func TestStorageOracleEquivalence(t *testing.T) {
	ctx := context.Background()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	memRepo := NewMemoryLedgerRepository()
	memRepo.SetTimeSource(func() time.Time { return fixedTime })
	store := storage.NewMemory()
	storageRepo := NewStorageLedgerRepository(store)
	storageRepo.SetTimeSource(func() time.Time { return fixedTime })
	if err := applyEventSequence(memRepo, fixedTime); err != nil {
		t.Fatalf("memory sequence: %v", err)
	}
	if err := applyEventSequence(storageRepo, fixedTime); err != nil {
		t.Fatalf("storage sequence: %v", err)
	}
	memSnap, err := memRepo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storSnap, err := storageRepo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotsEqual(memSnap, storSnap) {
		t.Logf("memory snapshot: run=%q status=%q tasks=%d", memSnap.RunID, memSnap.Status, len(memSnap.Tasks))
		t.Logf("storage snapshot: run=%q status=%q tasks=%d", storSnap.RunID, storSnap.Status, len(storSnap.Tasks))
		t.Fatal("MUTATION FAIL: run snapshots differ between memory and storage backends")
	}
	memTasks, err := memRepo.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storTasks, err := storageRepo.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(memTasks) != len(storTasks) {
		t.Fatalf("task count mismatch: memory=%d storage=%d", len(memTasks), len(storTasks))
	}
	memTaskMap := make(map[string]TaskSnapshot, len(memTasks))
	for _, t := range memTasks {
		memTaskMap[t.TaskID] = t
	}
	for _, s := range storTasks {
		m, ok := memTaskMap[s.TaskID]
		if !ok {
			t.Fatalf("task %q missing from memory backend", s.TaskID)
		}
		if m.Status != s.Status || m.Version != s.Version {
			t.Fatalf("task %q mismatch: memory=(%s,%d) storage=(%s,%d)",
				s.TaskID, m.Status, m.Version, s.Status, s.Version)
		}
	}

	memEvents, err := memRepo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storEvents, err := storageRepo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(memEvents) != len(storEvents) {
		t.Fatalf("event count mismatch: memory=%d storage=%d", len(memEvents), len(storEvents))
	}
	memEventMap := make(map[string]LifecycleEvent, len(memEvents))
	for _, e := range memEvents {
		memEventMap[e.ID] = e
	}
	for _, s := range storEvents {
		m, ok := memEventMap[s.ID]
		if !ok {
			t.Fatalf("event %q missing from memory backend", s.ID)
		}
		if m.Kind != s.Kind || m.TaskID != s.TaskID {
			t.Fatalf("event %q mismatch: memory=(%s,%s) storage=(%s,%s)",
				s.ID, m.Kind, m.TaskID, s.Kind, s.TaskID)
		}
	}

}

func TestStorageOracleEquivalence_Concurrent(t *testing.T) {
	// Concurrent stress test: both backends should handle concurrent CAS
	// with identical conflict/accept behaviour.
	ctx := context.Background()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	memRepo := NewMemoryLedgerRepository()
	memRepo.SetTimeSource(func() time.Time { return fixedTime })
	store := storage.NewMemory()
	storageRepo := NewStorageLedgerRepository(store)
	storageRepo.SetTimeSource(func() time.Time { return fixedTime })

	for _, tc := range []struct {
		name string
		repo LedgerRepository
	}{
		{"memory", memRepo},
		{"storage", storageRepo},
	} {
		runID := "run-" + tc.name
		if err := tc.repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			t.Fatalf("%s: CreateRun: %v", tc.name, err)
		}
		for i := 1; i <= 5; i++ {
			tid := fmt.Sprintf("t%d", i)
			if err := tc.repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: tid, Status: string(TaskStatusQueued), Version: 1}); err != nil {
				t.Fatalf("%s: CreateTask %s: %v", tc.name, tid, err)
			}
		}
		// Concurrent CAS: 10 goroutines race to transition each task
		var wg sync.WaitGroup
		for i := 1; i <= 5; i++ {
			tid := fmt.Sprintf("t%d", i)
			for j := 0; j < 10; j++ {
				wg.Add(1)
				go func(tid string) {
					defer wg.Done()
					_ = tc.repo.CompareAndSetTaskStatus(ctx, runID, tid, 1, string(TaskStatusRunning))
				}(tid)
			}
		}
		wg.Wait()

		// At least one task should have transitioned to running
		tasks, err := tc.repo.ListTasks(ctx, runID)
		if err != nil {
			t.Fatalf("%s: ListTasks: %v", tc.name, err)
		}
		hasRunning := false
		for _, task := range tasks {
			if task.Status == string(TaskStatusRunning) {
				hasRunning = true
				break
			}
		}
		if !hasRunning {
			t.Fatalf("%s: no tasks transitioned to running under concurrent CAS", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// SQLite crash-recovery: stale claim is NOT cleared on non-terminal runs
// (the holder may still be alive). Terminal runs' claims ARE cleared.
// ---------------------------------------------------------------------------

// TestStorageLedger_SQLite_RecoverDoesNotClearNonTerminalClaim verifies that
// Recover does NOT clear claims on RUNNING runs - a live concurrent holder
// must not lose its claim.
func TestStorageLedger_SQLite_RecoverDoesNotClearNonTerminalClaim(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_recover_nonterm.sqlite")

	store1, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store1)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-nonterm", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := store1.ClaimRun(ctx, "run-nonterm", "holder-one"); err != nil {
		t.Fatal(err)
	}
	if err := repo1.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo2 := NewStorageLedgerRepository(store2)
	repo2.SetTimeSource(func() time.Time { return now })
	if _, err := repo2.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// Non-terminal claim MUST survive Recover.
	if err := repo2.ClaimRun(ctx, "run-nonterm", "holder-two"); err == nil {
		t.Fatal("MUTATION FAIL: Recover cleared non-terminal claim (live holder loses protection)")
	}
	repo2.Close()
}

// TestStorageLedger_SQLite_RecoverClearsTerminalClaim verifies that Recover
// clears stale claims on TERMINAL (completed/failed/canceled) runs - the
// holder won't come back.
func TestStorageLedger_SQLite_RecoverClearsTerminalClaim(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_recover_term.sqlite")

	store1, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store1)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-term", Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := store1.ClaimRun(ctx, "run-term", "stale-holder"); err != nil {
		t.Fatal(err)
	}
	if err := repo1.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo2 := NewStorageLedgerRepository(store2)
	repo2.SetTimeSource(func() time.Time { return now })
	if _, err := repo2.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// Terminal claim MUST be cleared by Recover.
	if err := repo2.ClaimRun(ctx, "run-term", "new-holder"); err != nil {
		t.Fatalf("MUTATION FAIL: terminal run claim not cleared by Recover: %v", err)
	}
	repo2.Close()
}

// TestRecoverClassifiesWithoutMutatingRunStatus pins the reason the startup
// report is age-filtered rather than marked-once. There is no non-terminal
// "interrupted" status to write, and RunStatusCompleted/Failed/Canceled all make
// ResumeInterruptedRun refuse the run - so "mark it so we stop reporting it"
// would silently destroy the recoverability the report exists to advertise.
// Recover must classify only, leaving the run resumable across any number of
// launches, and must carry CreatedAt so callers can tell news from noise.
func TestRecoverClassifiesWithoutMutatingRunStatus(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	created := time.Date(2026, 7, 28, 19, 15, 3, 0, time.UTC)

	repo := NewStorageLedgerRepository(store)
	repo.SetTimeSource(func() time.Time { return created })
	if err := repo.CreateRun(ctx, "", RunSnapshot{
		RunID: "run-abandoned", DisplayName: "audit", Status: RunStatusQueued, CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{
		RunID: "run-abandoned", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Recover repeatedly: the classification must be stable, not consumed.
	for i := 1; i <= 3; i++ {
		recovered, err := repo.Recover(ctx)
		if err != nil {
			t.Fatalf("recover %d: %v", i, err)
		}
		var found *RecoveredRun
		for j := range recovered {
			if recovered[j].RunID == "run-abandoned" {
				found = &recovered[j]
			}
		}
		if found == nil {
			t.Fatalf("recover %d: run disappeared from the classification", i)
		}
		if !found.WasInterrupted {
			t.Fatalf("recover %d: run must stay classified as interrupted", i)
		}
		if isRunTerminal(found.Status) {
			t.Fatalf("recover %d: Recover made the run terminal (%s) - it can no longer be resumed",
				i, found.Status)
		}
		if !found.CreatedAt.Equal(created) {
			t.Fatalf("recover %d: CreatedAt = %v, want %v", i, found.CreatedAt, created)
		}
	}

	// And the stored snapshot itself is untouched.
	snap, err := repo.GetRun(ctx, "run-abandoned")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != RunStatusQueued {
		t.Fatalf("stored run status = %s, want %s (Recover must not mutate)", snap.Status, RunStatusQueued)
	}
}
