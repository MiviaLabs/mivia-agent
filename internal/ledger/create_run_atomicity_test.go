package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// failingAppendStore wraps a store and fails the first N run_created appends
// WITHOUT writing anything to the inner store. Later appends succeed. This is
// the clean-failure seam for the CreateRun atomicity tests: a failed durable
// append must leave no run_created row behind.
type failingAppendStore struct {
	inner storage.Store

	mu             sync.Mutex
	failRunCreated int
}

func (f *failingAppendStore) Append(ctx context.Context, e storage.Event) error {
	f.mu.Lock()
	fail := e.Kind == storageKindRunCreated && f.failRunCreated > 0
	if fail {
		f.failRunCreated--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("injected run_created append failure")
	}
	return f.inner.Append(ctx, e)
}

func (f *failingAppendStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	f.mu.Lock()
	fail := e.Kind == storageKindRunCreated && f.failRunCreated > 0
	if fail {
		f.failRunCreated--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("injected run_created append failure")
	}
	return f.inner.AppendClaimed(ctx, e, holder)
}

func (f *failingAppendStore) Events(ctx context.Context, runID string) ([]storage.Event, error) {
	return f.inner.Events(ctx, runID)
}

func (f *failingAppendStore) EventsSince(ctx context.Context, runID string, afterSequence int) ([]storage.Event, error) {
	return f.inner.EventsSince(ctx, runID, afterSequence)
}

func (f *failingAppendStore) DeleteRun(ctx context.Context, runID string, throughSequence int) error {
	return f.inner.DeleteRun(ctx, runID, throughSequence)
}

func (f *failingAppendStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	return f.inner.Changes(ctx, afterCursor)
}

func (f *failingAppendStore) ClaimRun(ctx context.Context, runID, holder string) error {
	return f.inner.ClaimRun(ctx, runID, holder)
}

func (f *failingAppendStore) ReleaseClaim(ctx context.Context, runID, holder string) error {
	return f.inner.ReleaseClaim(ctx, runID, holder)
}

func (f *failingAppendStore) ClearClaim(ctx context.Context, runID string) error {
	return f.inner.ClearClaim(ctx, runID)
}

func (f *failingAppendStore) PutContent(ctx context.Context, ref string, data []byte) error {
	return f.inner.PutContent(ctx, ref, data)
}

func (f *failingAppendStore) GetContent(ctx context.Context, ref string) ([]byte, error) {
	return f.inner.GetContent(ctx, ref)
}

func (f *failingAppendStore) Count(ctx context.Context) (int, error) { return f.inner.Count(ctx) }

func (f *failingAppendStore) TakeoverClaim(ctx context.Context, runID, holder string) error {
	return f.inner.TakeoverClaim(ctx, runID, holder)
}

func (f *failingAppendStore) ListRunIDs(ctx context.Context) ([]string, error) {
	return f.inner.ListRunIDs(ctx)
}

func (f *failingAppendStore) Close() error { return f.inner.Close() }

// TestCreateRunAtomicityOnAppendFailure pins D4 from the key's perspective:
// CreateRun must be atomic with respect to the idempotency key. When the
// durable append fails, the key must be left free - neither in the in-memory
// projection nor in the store - so a retry with the same key creates a fresh
// run instead of deduping onto a phantom.
//
// This is the regression guard for the layer-1 fix (storage.go CreateRun):
// register in the projection BEFORE the append, and roll the registration back
// if the append fails. A mem-first implementation without the rollback leaves
// the key registered after the failed append, and the retry below would fail
// with ErrDuplicate.
func TestCreateRunAtomicityOnAppendFailure(t *testing.T) {
	ctx := context.Background()
	store := &failingAppendStore{inner: storage.NewMemory(), failRunCreated: 1}
	repo := NewStorageLedgerRepository(store)

	// (a) The durable append fails -> CreateRun returns an error.
	if err := repo.CreateRun(ctx, "K", RunSnapshot{RunID: "run-x", Status: RunStatusCreated}); err == nil {
		t.Fatal("CreateRun succeeded despite a failed durable append")
	}

	// The key must not resolve in the projection after the failed append.
	if _, err := repo.GetRunByIdempotencyKey(ctx, "K"); err != ErrNotFound {
		t.Fatalf("GetRunByIdempotencyKey after failed CreateRun: got %v, want ErrNotFound (the key must not be registered)", err)
	}
	if _, err := repo.GetRun(ctx, "run-x"); err != ErrNotFound {
		t.Fatalf("GetRun after failed CreateRun: got %v, want ErrNotFound (the run must not be registered)", err)
	}

	// (b) The run_created row must not be in the store: a failed CreateRun
	// leaves no durable trace, so a later restart cannot replay a phantom key.
	events, err := store.inner.Events(ctx, "run-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range events {
		if evt.Kind == storageKindRunCreated {
			t.Fatalf("store contains a run_created row for %q after a failed CreateRun: %+v", "run-x", evt)
		}
	}

	// (a continued) A retry with the same key succeeds - no phantom hijack.
	if err := repo.CreateRun(ctx, "K", RunSnapshot{RunID: "run-y", Status: RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun retry with the same key after a failed attempt: %v", err)
	}
	got, err := repo.GetRunByIdempotencyKey(ctx, "K")
	if err != nil {
		t.Fatalf("GetRunByIdempotencyKey after retry: %v", err)
	}
	if got.RunID != "run-y" {
		t.Fatalf("retried run = %q, want %q", got.RunID, "run-y")
	}
}

// TestCreateRunAppendFailureLeavesNoDurableRowAcrossRestart is the restart
// form of the ordering guarantee: a fresh repository over the same store (a
// process restart) must not replay any keyed run_created row after the failed
// CreateRun. A durable phantom row is precisely what D4's crash window left
// behind and what layer 1 must make structurally impossible.
func TestCreateRunAppendFailureLeavesNoDurableRowAcrossRestart(t *testing.T) {
	ctx := context.Background()
	store := &failingAppendStore{inner: storage.NewMemory(), failRunCreated: 1}
	repo := NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "K", RunSnapshot{RunID: "run-x", Status: RunStatusCreated}); err == nil {
		t.Fatal("CreateRun succeeded despite a failed durable append")
	}

	// A fresh repository over the same store sees no keyed run and can create
	// one under the reclaimed key.
	fresh := NewStorageLedgerRepository(store)
	if _, err := fresh.GetRunByIdempotencyKey(ctx, "K"); err != ErrNotFound {
		t.Fatalf("fresh repo resolves key after failed CreateRun: got %v, want ErrNotFound", err)
	}
	if err := fresh.CreateRun(ctx, "K", RunSnapshot{RunID: "run-z", Status: RunStatusCreated}); err != nil {
		t.Fatalf("fresh repo CreateRun with the reclaimed key: %v", err)
	}
}

// TestCreateRunDuplicateKeyLeavesNoDurableRow pins the durable side of the
// ordering guarantee on the duplicate path. Under the pre-fix order the append
// ran before the in-memory registration, so a CreateRun that failed with
// ErrDuplicate (key already taken) had already written its own run_created row
// - a durable phantom (keyed row with no live projection record) that a later
// restart replays. The fixed order registers the key first, so a duplicate is
// refused before any append and the store stays clean.
func TestCreateRunDuplicateKeyLeavesNoDurableRow(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)

	if err := repo.CreateRun(ctx, "K", RunSnapshot{RunID: "run-x", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, "K", RunSnapshot{RunID: "run-y", Status: RunStatusCreated}); err != ErrDuplicate {
		t.Fatalf("second CreateRun with the same key: got %v, want ErrDuplicate", err)
	}

	// The refused CreateRun must not have appended a durable row: a failed
	// CreateRun leaves no trace in the store.
	events, err := store.Events(ctx, "run-y")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("store contains %d durable rows for the refused run %q: %+v", len(events), "run-y", events)
	}
}
