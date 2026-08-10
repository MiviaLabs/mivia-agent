package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestDeletedRunDoesNotResurrectInNextProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewStorageLedgerRepository(reopened)
	t.Cleanup(func() { _ = replayed.Close() })
	if _, err := replayed.GetRun(ctx, "deleted"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun after replay error = %v, want ErrNotFound", err)
	}
}

func TestStorageLedgerDeleteRunRetriesAfterPhysicalDeleteFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failAtomicDeleteStore{Store: store, remaining: 1}
	repo := NewStorageLedgerRepository(failing)
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); !errors.Is(err, errAtomicDelete) {
		t.Fatalf("DeleteRun with injected failure = %v, want physical delete error", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close failed store: %v", err)
	}
	reopenedStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopened := NewStorageLedgerRepository(reopenedStore)
	if err := reopened.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatalf("DeleteRun retry after reopen: %v", err)
	}
	events, err := reopenedStore.Events(ctx, "deleted")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != storageKindRunDeleted {
		t.Fatalf("events after retry = %#v, want only the deletion tombstone", events)
	}
}

func TestStorageLedgerDeleteRunFailureReleasesInflightSequence(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	failing := &failAtomicDeleteStore{Store: store, remaining: 1}
	reader := NewStorageLedgerRepository(failing)
	if err := reader.CreateRun(ctx, "", RunSnapshot{RunID: "run", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := reader.DeleteRun(ctx, "run"); !errors.Is(err, errAtomicDelete) {
		t.Fatalf("DeleteRun with injected failure = %v, want physical delete error", err)
	}

	writer := NewStorageLedgerRepository(store)
	if err := writer.CreateTask(ctx, TaskSnapshot{RunID: "run", TaskID: "task", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("CreateTask after failed delete: %v", err)
	}
	if _, err := reader.GetTask(ctx, "run", "task"); err != nil {
		t.Fatalf("GetTask after catch-up: %v", err)
	}
}

var errAtomicDelete = errors.New("injected atomic delete failure")

type failAtomicDeleteStore struct {
	storage.Store
	remaining int
}

func (s *failAtomicDeleteStore) AppendAndDeleteRun(ctx context.Context, tombstone storage.Event, claim storage.Claim) error {
	if s.remaining > 0 {
		s.remaining--
		return errAtomicDelete
	}
	return s.Store.AppendAndDeleteRun(ctx, tombstone, claim)
}

func TestDeleteRunKeepsChangesCursorMonotonic(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := NewStorageLedgerRepository(store)
	reader := NewStorageLedgerRepository(store)
	if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: "survives", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "survives"); err != nil {
		t.Fatalf("reader missed run appended after deletion: %v", err)
	}
}

func TestDeleteRunConvergesInASecondReader(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := NewStorageLedgerRepository(store)
	reader := NewStorageLedgerRepository(store)
	if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "deleted"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second reader GetRun error = %v, want ErrNotFound", err)
	}
}

func TestDeleteRunAllowsSameIDToBeRecreatedAndCaughtUp(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := NewStorageLedgerRepository(store)
	reader := NewStorageLedgerRepository(store)
	if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: "recreated", DisplayName: "first", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "recreated"); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteRun(ctx, "recreated"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, "recreated"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reader deletion error = %v, want ErrNotFound", err)
	}
	if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: "recreated", DisplayName: "second", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	got, err := reader.GetRun(ctx, "recreated")
	if err != nil {
		t.Fatalf("reader missed recreated run: %v", err)
	}
	if got.DisplayName != "second" {
		t.Fatalf("recreated run name = %q, want second", got.DisplayName)
	}
}

func TestDeleteRunLeavesContentUntouched(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreContent(ctx, "ref:output:shared", []byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.LoadContent(ctx, "ref:output:shared")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Fatalf("content = %q, want content", got)
	}
}

func TestDeleteRunOnMemoryBackend(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, "deleted")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != storageKindRunDeleted {
		t.Fatalf("events after delete = %#v, want one tombstone", events)
	}
	if _, cursor, err := store.Changes(ctx, 0); err != nil || cursor == 0 {
		t.Fatalf("Changes cursor = %d, err = %v; want monotonic non-zero cursor", cursor, err)
	}
}

func TestRebuildProjectionHonorsRunDeletion(t *testing.T) {
	payload, err := marshalRunSnapshot(RunSnapshot{RunID: "deleted", Status: RunStatusCreated})
	if err != nil {
		t.Fatal(err)
	}
	run, tasks, events, err := RebuildProjection([]storage.Event{
		{ID: "created", RunID: "deleted", Sequence: 1, Kind: storageKindRunCreated, Payload: payload},
		{ID: "deleted", RunID: "deleted", Sequence: 2, Kind: storageKindRunDeleted, Payload: []byte(`{"run_id":"deleted"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != "" || len(tasks) != 0 || len(events) != 0 {
		t.Fatalf("rebuild retained deleted state: run=%#v tasks=%#v events=%#v", run, tasks, events)
	}
}

func TestRecoverDoesNotReportDeletedRunAsInterrupted(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 0 {
		t.Fatalf("Recover returned deleted run: %#v", recovered)
	}
}

func TestDeleteRunClearsProjectionWatermarks(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "deleted", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	if _, ok := repo.applied["deleted"]; ok {
		t.Fatal("applied watermark retained deleted run")
	}
	if _, ok := repo.allocated["deleted"]; ok {
		t.Fatal("allocated watermark retained deleted run")
	}
	for key := range repo.inflight {
		if key.runID == "deleted" {
			t.Fatal("inflight watermark retained deleted run")
		}
	}
}

func TestUnknownStorageKindIsIgnored(t *testing.T) {
	repo := NewStorageLedgerRepository(storage.NewMemory())
	repo.mu.Lock()
	err := repo.applyStoreEventLocked(context.Background(), storage.Event{RunID: "unknown", Kind: "future_kind", Payload: []byte("x")})
	repo.mu.Unlock()
	if err != nil {
		t.Fatalf("unknown kind returned error: %v", err)
	}
	if _, _, _, err := RebuildProjection([]storage.Event{{RunID: "unknown", Kind: "future_kind", Payload: []byte("x")}}); err != nil {
		t.Fatalf("RebuildProjection unknown kind returned error: %v", err)
	}
}
