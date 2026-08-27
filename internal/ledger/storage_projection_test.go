package ledger

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ghostTailStore reports a run as moved in Changes but returns no tail events,
// so catchUp takes the "pending is empty" fast path: it advances the cursor
// and returns without folding anything. A real store only produces this shape
// in a race where the run is deleted between the freshness probe and the tail
// read, so the test store drives it deterministically.
type ghostTailStore struct {
	storage.Store
	runID  string
	maxSeq int
}

func (g *ghostTailStore) Changes(_ context.Context, _ uint64) (map[string]int, uint64, error) {
	return map[string]int{g.runID: g.maxSeq}, uint64(g.maxSeq), nil
}

func (g *ghostTailStore) EventsSince(context.Context, string, int) ([]storage.Event, error) {
	return nil, nil
}

func TestCatchUpAdvancesCursorWhenTailReadIsEmpty(t *testing.T) {
	ctx := context.Background()
	store := &ghostTailStore{Store: storage.NewMemory(), runID: "ghost-run", maxSeq: 4}
	repo := NewStorageLedgerRepository(store)

	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("ListRuns: got %d runs, want 0", len(runs))
	}
	// Even though no events were applied, the empty tail must still move the
	// cursor so the next probe does not re-read the same ghost run.
	cursor := repo.engine.Watermarks().Cursor()
	if cursor != uint64(store.maxSeq) {
		t.Fatalf("cursor = %d, want %d (an empty tail must still advance)", cursor, store.maxSeq)
	}
}

func TestCatchUpPropagatesApplyError(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	// A run_created row whose payload is not JSON makes applyStoreEventLocked
	// fail; catchUp must wrap and surface that error rather than swallow it.
	if err := store.Append(ctx, storage.Event{
		ID: "se-corrupt", RunID: "corrupt-run", Sequence: 1,
		Kind: storageKindRunCreated, Payload: []byte("not-json"),
	}); err != nil {
		t.Fatalf("append corrupt event: %v", err)
	}
	repo := NewStorageLedgerRepository(store)

	if _, err := repo.ListRuns(ctx); err == nil {
		t.Fatal("ListRuns over a corrupt run_created payload: got nil, want error")
	} else if !strings.Contains(err.Error(), "corrupt-run") {
		t.Fatalf("error %q does not name the failing run", err)
	}
}

func TestApplyTailSortTiesOnSequence(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())

	created, err := marshalRunSnapshot(RunSnapshot{RunID: "tie-run", Status: RunStatusCreated})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := marshalRunClosed("", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Two events sharing one RowID force applyTail's stable sort to fall
	// through to the sequence tie-break. RowID equality cannot arise from a
	// real store (rowids are unique per append), so it is exercised directly.
	events := []storage.Event{
		{ID: "tie-1", RunID: "tie-run", Sequence: 1, Kind: storageKindRunCreated, Payload: created, RowID: 5},
		{ID: "tie-2", RunID: "tie-run", Sequence: 2, Kind: storageKindRunClosed, Payload: closed, RowID: 5},
	}
	if err := repo.applyTail(ctx, events); err != nil {
		t.Fatalf("applyTail: %v", err)
	}

	snap, err := repo.GetRun(ctx, "tie-run")
	if err != nil {
		t.Fatalf("GetRun after applyTail: %v", err)
	}
	if snap.RunID != "tie-run" {
		t.Fatalf("run = %q, want tie-run", snap.RunID)
	}
}
