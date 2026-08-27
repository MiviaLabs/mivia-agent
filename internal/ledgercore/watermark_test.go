package ledgercore

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestWatermarkTracker_NextSequenceAndAdvance(t *testing.T) {
	wt := NewWatermarkTracker()

	runID := "run-test"
	s1 := wt.NextSequence(runID)
	if s1 != 1 {
		t.Fatalf("expected s1 = 1, got %d", s1)
	}
	s2 := wt.NextSequence(runID)
	if s2 != 2 {
		t.Fatalf("expected s2 = 2, got %d", s2)
	}

	wt.SetAllocated(runID, 10)
	if wt.Allocated(runID) != 10 {
		t.Fatalf("expected allocated = 10, got %d", wt.Allocated(runID))
	}
	// Lower sequence ignored
	wt.SetAllocated(runID, 5)
	if wt.Allocated(runID) != 10 {
		t.Fatalf("expected allocated = 10 after lower set, got %d", wt.Allocated(runID))
	}

	wt.SetApplied(runID, 2)
	if wt.Applied(runID) != 2 {
		t.Fatalf("expected applied = 2, got %d", wt.Applied(runID))
	}

	// Cursor advancement
	if wt.Cursor() != 0 {
		t.Fatalf("expected cursor = 0, got %d", wt.Cursor())
	}
	wt.AdvanceCursor(10)
	if wt.Cursor() != 10 {
		t.Fatalf("expected cursor = 10, got %d", wt.Cursor())
	}
	// Cursor never rewinds
	wt.AdvanceCursor(5)
	if wt.Cursor() != 10 {
		t.Fatalf("expected cursor = 10 after smaller advance, got %d", wt.Cursor())
	}
}

func TestWatermarkTracker_CheckBehind(t *testing.T) {
	wt := NewWatermarkTracker()
	wt.SetApplied("run-1", 5)
	wt.SetApplied("run-2", 10)

	maxSeqs := map[string]int{
		"run-1": 6,  // behind
		"run-2": 10, // up to date
		"run-3": 2,  // behind (applied is 0)
	}

	behind := wt.CheckBehind(maxSeqs)
	if len(behind) != 2 {
		t.Fatalf("expected 2 behind runs, got %v", behind)
	}
	if behind[0] != "run-1" || behind[1] != "run-3" {
		t.Fatalf("expected sorted behind runs ['run-1', 'run-3'], got %v", behind)
	}
}

func TestWatermarkTracker_RebaseRunSequence(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	runID := "run-rebase"

	// Append some events directly to store (non-empty payload required by store)
	if err := store.Append(ctx, storage.Event{ID: "e1", RunID: runID, Sequence: 1, Payload: []byte("p1")}); err != nil {
		t.Fatalf("store.Append e1: %v", err)
	}
	if err := store.Append(ctx, storage.Event{ID: "e2", RunID: runID, Sequence: 4, Payload: []byte("p2")}); err != nil {
		t.Fatalf("store.Append e2: %v", err)
	}

	wt := NewWatermarkTracker()
	if err := wt.RebaseRunSequence(ctx, store, runID); err != nil {
		t.Fatalf("RebaseRunSequence failed: %v", err)
	}

	if wt.Applied(runID) != 4 {
		t.Fatalf("expected applied = 4, got %d", wt.Applied(runID))
	}
	if wt.Allocated(runID) != 4 {
		t.Fatalf("expected allocated = 4, got %d", wt.Allocated(runID))
	}

	next := wt.NextSequence(runID)
	if next != 5 {
		t.Fatalf("expected next = 5, got %d", next)
	}

	// Error path
	errStore := &plainStore{err: errors.New("boom")}
	if err := wt.RebaseRunSequence(ctx, errStore, runID); err == nil {
		t.Fatalf("expected error from RebaseRunSequence with failing store")
	}

	wt.DeleteRun(runID)
	if wt.Applied(runID) != 0 || wt.Allocated(runID) != 0 {
		t.Fatalf("expected 0 after DeleteRun, got applied=%d, allocated=%d", wt.Applied(runID), wt.Allocated(runID))
	}
}
