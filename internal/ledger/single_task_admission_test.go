package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func queuedSingleAdmission(key, runID, taskID string) SingleTaskAdmission {
	now := time.Now().UTC()
	return SingleTaskAdmission{
		IdempotencyKey: key,
		Run:            RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: now},
		Task: TaskSnapshot{RunID: runID, TaskID: taskID, Status: string(TaskStatusQueued), CreatedAt: now,
			Attempts: []AttemptSnapshot{{AttemptID: "attempt-" + taskID, RunID: runID, TaskID: taskID, AttemptNum: 1, Status: string(TaskStatusQueued), StartedAt: now}}},
	}
}

func TestSingleTaskAdmissionCreatesCompleteTuple(t *testing.T) {
	memory := NewStorageLedgerRepository(storage.NewMemory())
	sqliteStore, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for name, repo := range map[string]LedgerRepository{"memory": memory, "sqlite": NewStorageLedgerRepository(sqliteStore)} {
		t.Run(name, func(t *testing.T) {
			err := repo.AdmitSingleTask(context.Background(), queuedSingleAdmission("panel-child", "run-panel", "task-panel"))
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.GetRunByIdempotencyKey(context.Background(), "panel-child")
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := repo.ListTasks(context.Background(), run.RunID)
			if err != nil || len(tasks) != 1 || tasks[0].TaskID != "task-panel" {
				t.Fatalf("tuple = %+v, tasks=%+v, err=%v", run, tasks, err)
			}
		})
	}
}

func TestSingleTaskAdmissionRaceHasOneWinner(t *testing.T) {
	store := storage.NewMemory()
	assertSingleTaskAdmissionRace(t, store, store)
}

func TestSQLiteSingleTaskAdmissionRaceHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	first, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := storage.OpenSQLite(path)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	assertSingleTaskAdmissionRace(t, first, second)
}

func TestSingleTaskAdmissionRejectsExistingRunAndClaim(t *testing.T) {
	for name, store := range map[string]storage.Store{
		"memory": storage.NewMemory(),
		"sqlite": func() storage.Store {
			s, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			repo := NewStorageLedgerRepository(store)
			first := queuedSingleAdmission("first", "run-fixed", "task-fixed")
			if err := repo.AdmitSingleTask(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			second := queuedSingleAdmission("second", "run-fixed", "task-other")
			if err := repo.AdmitSingleTask(context.Background(), second); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("existing run error = %v, want ErrDuplicate", err)
			}
			tasks, err := repo.ListTasks(context.Background(), first.Run.RunID)
			if err != nil || len(tasks) != 1 || tasks[0].TaskID != first.Task.TaskID {
				t.Fatalf("tasks=%+v err=%v", tasks, err)
			}
			claimed := queuedSingleAdmission("claimed", "run-claimed", "task-claimed")
			if err := store.ClaimRun(context.Background(), claimed.Run.RunID, "other"); err != nil {
				t.Fatal(err)
			}
			if err := repo.AdmitSingleTask(context.Background(), claimed); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("claimed run error = %v, want ErrClaimHeld", err)
			}
		})
	}
}

func assertSingleTaskAdmissionRace(t *testing.T, firstStore, secondStore storage.Store) {
	t.Helper()
	first := NewStorageLedgerRepository(firstStore)
	second := NewStorageLedgerRepository(secondStore)
	admission := queuedSingleAdmission("panel-race", "run-panel-race", "task-panel-race")
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, repo := range []*StorageLedgerRepository{first, second} {
		wg.Add(1)
		go func(repo *StorageLedgerRepository) {
			defer wg.Done()
			errs <- repo.AdmitSingleTask(context.Background(), admission)
		}(repo)
	}
	wg.Wait()
	close(errs)
	var success, duplicate int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrDuplicate) {
			duplicate++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || duplicate != 1 {
		t.Fatalf("success=%d duplicate=%d", success, duplicate)
	}
	observer := NewStorageLedgerRepository(firstStore)
	tasks, err := observer.ListTasks(context.Background(), admission.Run.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	// The losing repository must release its local sequence reservations. It
	// must replay the winning atomic batch instead of treating it as inflight.
	for _, repo := range []*StorageLedgerRepository{first, second} {
		got, err := repo.GetRunByIdempotencyKey(context.Background(), admission.IdempotencyKey)
		if err != nil || got.RunID != admission.Run.RunID {
			t.Fatalf("loser replay run=%+v err=%v", got, err)
		}
	}
}
