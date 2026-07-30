package ledger

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Event-projection rebuild coverage, split out of storage_test.go to keep both
// files inside the structure gate.

// TestListEventsRestoresKindAfterProjectionRebuild covers the rebuild path used
// by any run that existed before the current process started: the events are
// replayed out of the store rather than seeded in-process by AppendEvent.
//
// The reason this is load-bearing: a LifecycleEvent that comes back with an
// empty Kind makes a caller's kind filter match zero rows, which is
// indistinguishable from "no such events ever happened". A read-only history
// tool built on ListEvents would silently report an empty history for every run
// it did not itself create.
func TestListEventsRestoresKindAfterProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "events_rebuild.sqlite")

	store1, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	repo1 := NewStorageLedgerRepository(store1)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}

	want := []LifecycleEvent{
		{ID: "ev-1", RunID: "run-1", Kind: "run_started"},
		{ID: "ev-2", RunID: "run-1", Kind: "task_started", TaskID: "t1", AttemptID: "a1"},
		{ID: "ev-3", RunID: "run-1", Kind: "task_completed", TaskID: "t1", AttemptID: "a1",
			Payload: []byte(`{"exit":0}`)},
		{ID: "ev-4", RunID: "run-1", Kind: "run_completed", TaskID: "", AttemptID: ""},
	}
	for _, evt := range want {
		if err := repo1.AppendEvent(ctx, evt); err != nil {
			t.Fatalf("append %s: %v", evt.ID, err)
		}
	}
	if err := repo1.Close(); err != nil {
		t.Fatalf("close repo1: %v", err)
	}

	// Reopen the same database file with a brand-new repository: this is what a
	// fresh process sees, and it forces the projection rebuild path.
	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	repo2 := NewStorageLedgerRepository(store2)
	defer func() { _ = repo2.Close() }()

	got, err := repo2.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents after rebuild: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ListEvents returned %d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Kind != w.Kind {
			t.Errorf("event %d: Kind = %q, want %q", i, g.Kind, w.Kind)
		}
		if g.TaskID != w.TaskID {
			t.Errorf("event %d: TaskID = %q, want %q", i, g.TaskID, w.TaskID)
		}
		if g.AttemptID != w.AttemptID {
			t.Errorf("event %d: AttemptID = %q, want %q", i, g.AttemptID, w.AttemptID)
		}
		if g.RunID != "run-1" {
			t.Errorf("event %d: RunID = %q, want %q", i, g.RunID, "run-1")
		}
		if string(g.Payload) != string(w.Payload) {
			t.Errorf("event %d: Payload = %q, want %q", i, g.Payload, w.Payload)
		}
	}

	// A kind filter over rebuilt events must still select rows.
	var completed int
	for _, g := range got {
		if g.Kind == "task_completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Errorf("kind filter %q matched %d events, want 1", "task_completed", completed)
	}
}

// TestListEventsTimestampsAreReplayRelative DOCUMENTS A KNOWN LIMITATION rather
// than endorsing it: the identifying fields of a lifecycle event survive a
// projection rebuild, but CreatedAt does not. AppendEvent marshals the caller's
// event into the row payload before mem.AppendEvent stamps Sequence/CreatedAt,
// and the replay path re-enters mem.AppendEvent, which stamps them again. So a
// history reader looking at a replayed run sees the REPLAY instant, not when the
// event happened.
//
// This test exists so the surprise is visible here instead of being rediscovered
// by whoever trusts a replayed timestamp. If a later change makes the original
// timestamp durable, this test is expected to fail and should be inverted.
func TestListEventsTimestampsAreReplayRelative(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "events_timestamps.sqlite")

	store1, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	repo1 := NewStorageLedgerRepository(store1)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	want := LifecycleEvent{
		ID: "ev-1", RunID: "run-1", Kind: "task_completed",
		TaskID: "t1", AttemptID: "a1", Payload: []byte(`{"exit":0}`),
	}
	if err := repo1.AppendEvent(ctx, want); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := repo1.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents before rebuild: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("ListEvents before rebuild returned %d events, want 1", len(before))
	}
	original := before[0].CreatedAt
	if original.IsZero() {
		t.Fatalf("in-process CreatedAt is zero, want the append instant")
	}
	if err := repo1.Close(); err != nil {
		t.Fatalf("close repo1: %v", err)
	}

	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	repo2 := NewStorageLedgerRepository(store2)
	defer func() { _ = repo2.Close() }()
	got, err := repo2.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents after rebuild: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEvents after rebuild returned %d events, want 1", len(got))
	}

	// The identifying fields DO survive the rebuild.
	if got[0].Kind != want.Kind || got[0].TaskID != want.TaskID || got[0].AttemptID != want.AttemptID {
		t.Errorf("rebuilt event = {Kind:%q TaskID:%q AttemptID:%q}, want {Kind:%q TaskID:%q AttemptID:%q}",
			got[0].Kind, got[0].TaskID, got[0].AttemptID, want.Kind, want.TaskID, want.AttemptID)
	}
	if string(got[0].Payload) != string(want.Payload) {
		t.Errorf("rebuilt Payload = %q, want %q", got[0].Payload, want.Payload)
	}
	// The timestamp does NOT: it is re-stamped at replay time.
	if got[0].CreatedAt.Equal(original) {
		t.Errorf("rebuilt CreatedAt = %v, equal to the original append time — the "+
			"documented replay-relative behaviour changed; see fromStorageEvent and invert this test",
			got[0].CreatedAt)
	}
	if got[0].CreatedAt.Before(original) {
		t.Errorf("rebuilt CreatedAt = %v is before the original append time %v", got[0].CreatedAt, original)
	}
	// Sequence is likewise re-derived from replay order, not restored.
	if got[0].Sequence != 1 {
		t.Errorf("rebuilt Sequence = %d, want 1 (re-derived from replay order)", got[0].Sequence)
	}
}

// TestListEventsToleratesUndecodablePayload checks that a stored lifecycle
// event whose payload is not a marshalled LifecycleEvent (foreign writer or a
// hand-edited row) degrades to the column data instead of failing the whole
// listing. Listing a run must never break on one bad row.
func TestListEventsToleratesUndecodablePayload(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "events_bad_payload.sqlite")

	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = store.Close() }()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.AppendEvent(ctx, LifecycleEvent{ID: "ev-1", RunID: "run-1", Kind: "run_started"}); err != nil {
		t.Fatal(err)
	}

	// Write a lifecycle-kind row straight to the store with a payload that is
	// not valid JSON at all — the lowest-level write path available.
	bad := storage.Event{
		ID:       "se-bad",
		RunID:    "run-1",
		Sequence: 3,
		Kind:     storageKindLifecycleEvent,
		Payload:  []byte("not-a-marshalled-lifecycle-event"),
	}
	if err := store.Append(ctx, bad); err != nil {
		t.Fatalf("append raw store event: %v", err)
	}

	// A fresh repository over the same store replays every row, including the
	// undecodable one.
	repo2 := NewStorageLedgerRepository(store)
	got, err := repo2.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents with undecodable payload: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListEvents returned %d events, want 2", len(got))
	}
	var found bool
	for _, g := range got {
		if g.RunID != "run-1" {
			t.Errorf("event %+v: RunID = %q, want %q", g, g.RunID, "run-1")
		}
		if g.ID == "se-bad" {
			found = true
		}
	}
	if !found {
		t.Errorf("undecodable event was dropped from ListEvents: got %+v", got)
	}
}
