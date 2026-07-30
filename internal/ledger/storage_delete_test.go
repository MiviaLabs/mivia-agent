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
	if _, err := replayed.GetRun(ctx, "deleted"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun after replay error = %v, want ErrNotFound", err)
	}
}

func TestDeleteRunKeepsChangesCursorMonotonic(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
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
