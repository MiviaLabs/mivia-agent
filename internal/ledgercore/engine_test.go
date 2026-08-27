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

func TestEngineCatchUpSince(t *testing.T) {
	memStore := storage.NewMemory()
	engine := NewEngine(memStore, false, "h1")
	ctx := context.Background()

	_ = engine.AppendEvent(ctx, storage.Event{
		ID:       "ev-1",
		RunID:    "run-1",
		Sequence: 1,
		Kind:     "test",
		Payload:  []byte("1"),
	}, AppendOptions{})

	_ = engine.AppendEvent(ctx, storage.Event{
		ID:       "ev-2",
		RunID:    "run-2",
		Sequence: 1,
		Kind:     "test",
		Payload:  []byte("2"),
	}, AppendOptions{})

	// Reset watermarks on a second engine instance
	engine2 := NewEngine(memStore, false, "h2")
	var tail []storage.Event
	err := engine2.CatchUpSince(ctx, nil, func(ctx context.Context, events []storage.Event) error {
		tail = append(tail, events...)
		return nil
	})
	if err != nil {
		t.Fatalf("catch up since failed: %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("expected 2 events, got %d", len(tail))
	}

	// CatchUpSince with filter
	engine3 := NewEngine(memStore, false, "h3")
	var filteredTail []storage.Event
	err = engine3.CatchUpSince(ctx, func(runID string, maxSeq int) FilterDecision {
		if runID == "run-1" {
			return FilterAdvanceOnly
		}
		return FilterApply
	}, func(ctx context.Context, events []storage.Event) error {
		filteredTail = append(filteredTail, events...)
		return nil
	})
	if err != nil {
		t.Fatalf("filtered catch up since failed: %v", err)
	}
	if len(filteredTail) != 1 || filteredTail[0].RunID != "run-2" {
		t.Fatalf("expected 1 event for run-2, got %v", filteredTail)
	}
	if engine3.Watermarks().Applied("run-1") != 1 {
		t.Fatalf("expected run-1 watermark to advance to 1, got %d", engine3.Watermarks().Applied("run-1"))
	}
}

func TestSortEvents(t *testing.T) {
	events := []storage.Event{
		{ID: "e3", RowID: 3, Sequence: 2},
		{ID: "e1", RowID: 1, Sequence: 1},
		{ID: "e2", RowID: 2, Sequence: 1},
	}
	SortEvents(events)
	if events[0].ID != "e1" || events[1].ID != "e2" || events[2].ID != "e3" {
		t.Fatalf("unexpected sort order: %v", events)
	}

	eventsStable := []storage.Event{
		{ID: "e3", RowID: 0, Sequence: 3},
		{ID: "e1", RowID: 0, Sequence: 1},
		{ID: "e2", RowID: 0, Sequence: 2},
	}
	SortEventsStable(eventsStable)
	if eventsStable[0].ID != "e1" || eventsStable[1].ID != "e2" || eventsStable[2].ID != "e3" {
		t.Fatalf("unexpected stable sort order: %v", eventsStable)
	}
}

func TestEngineClaimsAndContentDelegation(t *testing.T) {
	memStore := storage.NewMemory()
	engine := NewEngine(memStore, false, "holder-a")
	ctx := context.Background()

	// Claims
	if err := engine.ClaimRun(ctx, "run-1", "holder-a"); err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if held, err := engine.IsRunHeld(ctx, "run-1"); err != nil || !held {
		t.Fatalf("expected run held, got held=%v, err=%v", held, err)
	}
	if holder, _, ok, err := engine.GetRunClaim(ctx, "run-1"); err != nil || !ok || holder != "holder-a" {
		t.Fatalf("unexpected claim: holder=%s, ok=%v, err=%v", holder, ok, err)
	}
	if err := engine.RefreshRunClaim(ctx, "run-1", "holder-a"); err != nil {
		t.Fatalf("refresh claim failed: %v", err)
	}
	if err := engine.TakeoverRunClaim(ctx, "run-1", "holder-b"); err != nil {
		t.Fatalf("takeover claim failed: %v", err)
	}
	if err := engine.TakeoverExpiredRunClaim(ctx, "run-1", "holder-c", 0); err != nil {
		t.Fatalf("takeover expired failed: %v", err)
	}
	if fenced, err := engine.IsRunTokenFenced(ctx, "run-1", "token-x"); err != nil || fenced {
		t.Fatalf("unexpected token fenced: %v, %v", fenced, err)
	}
	if err := engine.ReleaseRun(ctx, "run-1", "holder-c"); err != nil {
		t.Fatalf("release claim failed: %v", err)
	}
	if err := engine.ClearRunClaim(ctx, "run-1"); err != nil {
		t.Fatalf("clear claim failed: %v", err)
	}

	// Content
	if err := engine.StoreContent(ctx, "ref:test", []byte("sample")); err != nil {
		t.Fatalf("store content failed: %v", err)
	}
	data, err := engine.LoadContent(ctx, "ref:test")
	if err != nil || string(data) != "sample" {
		t.Fatalf("load content failed: %v, data=%s", err, string(data))
	}
}
