package ledgercore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestEngineLifecycle(t *testing.T) {
	memStore := storage.NewMemory()
	engine := NewEngine(memStore, true, "test-holder")

	if err := engine.CheckOpen(); err != nil {
		t.Fatalf("expected open, got: %v", err)
	}

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	engine.SetTimeSource(func() time.Time { return clock })
	if !engine.Now().Equal(clock) {
		t.Fatalf("expected clock %v, got %v", clock, engine.Now())
	}

	if engine.Store() != memStore {
		t.Fatalf("unexpected store pointer")
	}

	if engine.Claims().Holder() != "test-holder" {
		t.Fatalf("expected test-holder, got %s", engine.Claims().Holder())
	}

	if err := engine.Close(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	if !engine.IsClosed() {
		t.Fatalf("expected closed")
	}
	if err := engine.CheckOpen(); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestEngineSequenceAndCatchUp(t *testing.T) {
	memStore := storage.NewMemory()
	engine := NewEngine(memStore, false, "h1")
	ctx := context.Background()

	seq1 := engine.NextSequence("run-1")
	if seq1 != 1 {
		t.Fatalf("expected seq 1, got %d", seq1)
	}
	seq2 := engine.NextSequence("run-1")
	if seq2 != 2 {
		t.Fatalf("expected seq 2, got %d", seq2)
	}

	evt := storage.Event{
		ID:       "ev-1",
		RunID:    "run-1",
		Sequence: int(seq1),
		Kind:     "test_event",
		Payload:  []byte("hello"),
	}

	if err := engine.AppendEvent(ctx, evt, AppendOptions{}); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	rebuilt := make(map[string]int)
	err := engine.CatchUp(ctx, nil, func(ctx context.Context, runID string, events []storage.Event) error {
		rebuilt[runID] = len(events)
		return nil
	})
	if err != nil {
		t.Fatalf("catch up failed: %v", err)
	}
}

func TestEngineDuplicatePayloadResolution(t *testing.T) {
	memStore := storage.NewMemory()
	engine := NewEngine(memStore, false, "h1")
	ctx := context.Background()

	evt := storage.Event{
		ID:       "ev-uniq",
		RunID:    "run-1",
		Sequence: 1,
		Kind:     "test",
		Payload:  []byte("data-1"),
	}

	if err := engine.AppendEvent(ctx, evt, AppendOptions{}); err != nil {
		t.Fatalf("first append failed: %v", err)
	}

	// Idempotent retry with same payload
	err := engine.AppendEvent(ctx, evt, AppendOptions{
		OnDuplicate: func(ctx context.Context, e storage.Event) error {
			return engine.CheckDuplicatePayload(ctx, e)
		},
	})
	if err != nil {
		t.Fatalf("expected duplicate with same payload to succeed, got %v", err)
	}

	// Conflict with different payload
	evtConflict := evt
	evtConflict.Payload = []byte("data-2")
	err = engine.AppendEvent(ctx, evtConflict, AppendOptions{
		OnDuplicate: func(ctx context.Context, e storage.Event) error {
			return engine.CheckDuplicatePayload(ctx, e)
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}
