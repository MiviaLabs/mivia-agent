package ledger

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestIdempotencyKeyPersistsAcrossRepositoryRestart is the regression guard
// for finding F6 (HIGH): the idempotency key used to live only in the live
// projection's idemLookup map. A fresh repository over the same SQLite store
// (a process restart, or two processes sharing one workspace) replayed the
// run_created event WITHOUT the key, so CreateRun with the same key succeeded
// a second time and the same work executed twice.
//
// Repo A creates a run under key "K" and closes. Repo B is a fresh repository
// over the SAME file. It must refuse a second CreateRun with key "K" (the key
// must have been replayed into B's projection) and must resolve that key back
// to run A.
func TestIdempotencyKeyPersistsAcrossRepositoryRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "idempotency.db")

	// Repo A writes the run and its task, then closes (taking the store with it).
	storeA, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite (A): %v", err)
	}
	repoA := NewStorageLedgerRepository(storeA)
	if err := repoA.CreateRun(ctx, "K", RunSnapshot{RunID: "run-x", Status: RunStatusCreated}); err != nil {
		t.Fatalf("repo A CreateRun: %v", err)
	}
	if err := repoA.CreateTask(ctx, TaskSnapshot{RunID: "run-x", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("repo A CreateTask: %v", err)
	}
	if err := repoA.Close(); err != nil {
		t.Fatalf("repo A Close: %v", err)
	}

	// Repo B is a fresh repository over the same SQLite file.
	storeB, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite (B): %v", err)
	}
	repoB := NewStorageLedgerRepository(storeB)
	t.Cleanup(func() { _ = repoB.Close() })

	// The key must have been replayed: a second CreateRun with the same key but
	// a different run ID is refused as a duplicate.
	runY := RunSnapshot{RunID: "run-y", Status: RunStatusCreated}
	if err := repoB.CreateRun(ctx, "K", runY); err != ErrDuplicate {
		t.Fatalf("repo B CreateRun with reused key: got %v, want ErrDuplicate (idempotency key was not replayed into the fresh projection)", err)
	}

	// And the original run must be recoverable by that key after the restart.
	got, err := repoB.GetRunByIdempotencyKey(ctx, "K")
	if err != nil {
		t.Fatalf("repo B GetRunByIdempotencyKey: %v", err)
	}
	if got.RunID != "run-x" {
		t.Fatalf("repo B GetRunByIdempotencyKey: RunID = %q, want %q", got.RunID, "run-x")
	}
}

// TestRunSnapshotUnmarshalMissingIdempotencyKeyIsEmpty pins the backward
// compatibility contract: run_created payloads written before the key field
// existed decode to an empty key, so replay passes "" exactly as it always
// did - older stores keep their exact pre-fix behaviour.
func TestRunSnapshotUnmarshalMissingIdempotencyKeyIsEmpty(t *testing.T) {
	// A hand-written legacy payload without the field decodes to "".
	got, err := unmarshalRunSnapshot([]byte(`{"RunID":"run-legacy","Status":"created"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.IdempotencyKey != "" {
		t.Fatalf("legacy payload decoded IdempotencyKey = %q, want empty", got.IdempotencyKey)
	}
	// A keyed payload round-trips the key.
	roundTrip, err := unmarshalRunSnapshot([]byte(`{"RunID":"run-x","Status":"created","idempotency_key":"K"}`))
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.IdempotencyKey != "K" {
		t.Fatalf("keyed payload decoded IdempotencyKey = %q, want %q", roundTrip.IdempotencyKey, "K")
	}
}
