package ledger

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

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

// TestListEventsPreserveOriginalTimestampAcrossRebuild is the inversion of the
// former TestListEventsTimestampsAreReplayRelative, which asserted the defect
// this test now forbids: that a replayed lifecycle event carries the REPLAY
// instant instead of the instant it happened. That test said it was "expected to
// fail and should be inverted" once the timestamp became durable. This is that
// inversion, kept rather than deleted so the regression guard survives.
//
// The mechanism it pins: AppendEvent stamps CreatedAt BEFORE
// marshalLifecycleEvent, so the durable payload and the live projection carry
// one instant by construction; and fromStorageEvent hands that value back on
// replay instead of letting mem.AppendEvent re-stamp it.
//
// Both clocks are injected, so the assertion is exact equality rather than the
// inequality-with-slop the original test could manage. T1 is deliberately nine
// hours after T0: a re-stamp is unmistakable, not a rounding artefact.
func TestListEventsPreserveOriginalTimestampAcrossRebuild(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events_timestamps.sqlite")

	appendInstant := time.Date(2026, 7, 30, 9, 15, 30, 123456789, time.UTC)
	replayInstant := time.Date(2026, 7, 30, 18, 15, 30, 0, time.UTC)

	want := LifecycleEvent{
		ID: "ev-1", RunID: "run-1", Kind: "task_completed",
		TaskID: "t1", AttemptID: "a1", Payload: []byte(`{"exit":0}`),
	}
	seedEventUnderClock(t, dbPath, want, appendInstant)
	got := replayEventsUnderClock(t, dbPath, "run-1", replayInstant)
	if len(got) != 1 {
		t.Fatalf("ListEvents after rebuild returned %d events, want 1", len(got))
	}

	// The identifying fields survive, as they did before.
	if got[0].Kind != want.Kind || got[0].TaskID != want.TaskID || got[0].AttemptID != want.AttemptID {
		t.Errorf("rebuilt event = {Kind:%q TaskID:%q AttemptID:%q}, want {Kind:%q TaskID:%q AttemptID:%q}",
			got[0].Kind, got[0].TaskID, got[0].AttemptID, want.Kind, want.TaskID, want.AttemptID)
	}
	if string(got[0].Payload) != string(want.Payload) {
		t.Errorf("rebuilt Payload = %q, want %q", got[0].Payload, want.Payload)
	}
	// And now so does the timestamp — exactly, to the nanosecond.
	if !got[0].CreatedAt.Equal(appendInstant) {
		t.Errorf("rebuilt CreatedAt = %v, want the original append instant %v "+
			"(the replay clock is %v — a match with that means the projection re-stamped it)",
			got[0].CreatedAt, appendInstant, replayInstant)
	}
	// Sequence remains derived from replay order, which is store append order, so
	// the derived value is the live value. It is not restored from the payload.
	if got[0].Sequence != 1 {
		t.Errorf("rebuilt Sequence = %d, want 1 (re-derived from replay order)", got[0].Sequence)
	}
	// The event id is still the storage row id, not the caller's. Restoring the
	// caller's id would collide with the coordinator's process-local evt-N
	// counter and make a resumed run's own events duplicates; see plan 21 C2.
	if got[0].ID == want.ID {
		t.Errorf("rebuilt ID = %q, equal to the caller's — replayed events are "+
			"expected to report the storage row id (see plan 21 C2)", got[0].ID)
	}
}

// seedEventUnderClock writes one run and one lifecycle event to a fresh SQLite
// file with the repository clock pinned to instant, then closes the repository so
// the next reader is forced down the replay path. It asserts the in-process
// timestamp equals instant, so a later replay comparison cannot pass because the
// live value was already wrong.
func seedEventUnderClock(t *testing.T, dbPath string, event LifecycleEvent, instant time.Time) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	repo := NewStorageLedgerRepository(store)
	repo.SetTimeSource(func() time.Time { return instant })
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: event.RunID, Status: RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	before, err := repo.ListEvents(ctx, event.RunID)
	if err != nil {
		t.Fatalf("ListEvents before rebuild: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("ListEvents before rebuild returned %d events, want 1", len(before))
	}
	if !before[0].CreatedAt.Equal(instant) {
		t.Fatalf("in-process CreatedAt = %v, want the injected append instant %v",
			before[0].CreatedAt, instant)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close seed repo: %v", err)
	}
}

// replayEventsUnderClock opens the same file with a brand-new repository whose
// clock reads instant — what a fresh process sees — and lists the run's events,
// forcing the projection rebuild.
func replayEventsUnderClock(t *testing.T, dbPath, runID string, instant time.Time) []LifecycleEvent {
	t.Helper()
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	repo := NewStorageLedgerRepository(store)
	repo.SetTimeSource(func() time.Time { return instant })
	t.Cleanup(func() { _ = repo.Close() })
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListEvents after rebuild: %v", err)
	}
	return events
}

// TestAppendEventStampsBeforeMarshalling pins the ordering of two statements,
// which no rebuild test can do on its own. A rebuild test can be satisfied by
// stamping late and restoring from somewhere else; this one reads the durable row
// payload straight back out of the store and requires the timestamp to already be
// in it.
//
// That is the shape of the original defect: marshalLifecycleEvent ran on the
// caller's event, and only afterwards did mem.AppendEvent stamp it, so the
// durable copy always held "0001-01-01T00:00:00Z" while the live projection held
// a real instant. A value assigned after marshalling is a value that was never
// durable.
func TestAppendEventStampsBeforeMarshalling(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	defer func() { _ = repo.Close() }()

	instant := time.Date(2026, 7, 30, 9, 15, 30, 123456789, time.UTC)
	repo.SetTimeSource(func() time.Time { return instant })

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-1", RunID: "run-1", Kind: "task_completed", TaskID: "t1",
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	live, err := repo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("ListEvents returned %d events, want 1", len(live))
	}

	// Read the durable row back and decode the payload the store actually holds.
	rows, err := store.Events(ctx, "run-1")
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var found bool
	for _, row := range rows {
		if row.Kind != storageKindLifecycleEvent {
			continue
		}
		found = true
		decoded, err := unmarshalLifecycleEvent(row.Payload)
		if err != nil {
			t.Fatalf("decode stored payload %q: %v", row.Payload, err)
		}
		if decoded.CreatedAt.IsZero() {
			t.Errorf("stored payload CreatedAt is zero: %s\n"+
				"the event was marshalled before it was stamped, so the durable copy "+
				"holds no timestamp", row.Payload)
		}
		if !decoded.CreatedAt.Equal(live[0].CreatedAt) {
			t.Errorf("stored payload CreatedAt = %v, live ListEvents reported %v — "+
				"the durable copy and the projection must carry one instant",
				decoded.CreatedAt, live[0].CreatedAt)
		}
		if !decoded.CreatedAt.Equal(instant) {
			t.Errorf("stored payload CreatedAt = %v, want the injected clock %v",
				decoded.CreatedAt, instant)
		}
	}
	if !found {
		t.Fatal("no lifecycle_event row in the store")
	}
}

// TestLegacyRowWithoutTimestampFallsBackToReadInstant tests plan 21 §6's
// graceful-degradation claim instead of asserting it in prose. There is no schema
// version anywhere — no version table, no PRAGMA user_version, the DDL is an
// inline CREATE TABLE IF NOT EXISTS — so a database written by an earlier build
// is recognised by its content, not by a version gate.
//
// Such a row's payload holds "CreatedAt":"0001-01-01T00:00:00Z". The replay path
// must decline that value and let the projection stamp its own, which is exactly
// the behaviour those rows already had. No error, no crash, no zero timestamp
// leaking out to a history reader.
func TestLegacyRowWithoutTimestampFallsBackToReadInstant(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()

	readInstant := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)

	seed := NewStorageLedgerRepository(store)
	if err := seed.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}

	// Write a lifecycle row whose payload is a pre-plan-21 marshalled event: every
	// identifying field present, CreatedAt and Sequence zero. This is byte-for-byte
	// what the old AppendEvent produced.
	legacy := LifecycleEvent{ID: "evt-9", RunID: "run-1", Kind: "task_completed", TaskID: "t1"}
	payload, err := marshalLifecycleEvent(legacy)
	if err != nil {
		t.Fatalf("marshal legacy event: %v", err)
	}
	if !bytes.Contains(payload, []byte(`"CreatedAt":"0001-01-01T00:00:00Z"`)) {
		t.Fatalf("legacy payload does not carry a zero CreatedAt, so this test does "+
			"not exercise the fallback: %s", payload)
	}
	if err := store.Append(ctx, storage.Event{
		ID: "se-legacy", RunID: "run-1", Sequence: 500,
		Kind: storageKindLifecycleEvent, Payload: payload,
	}); err != nil {
		t.Fatalf("append legacy row: %v", err)
	}

	// A fresh repository replays it.
	repo := NewStorageLedgerRepository(store)
	repo.SetTimeSource(func() time.Time { return readInstant })
	defer func() { _ = repo.Close() }()

	got, err := repo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents over a legacy row: %v", err)
	}
	var legacyEvent *LifecycleEvent
	for i := range got {
		if got[i].Kind == "task_completed" {
			legacyEvent = &got[i]
		}
	}
	if legacyEvent == nil {
		t.Fatalf("the legacy row was dropped from ListEvents: %+v", got)
	}
	if legacyEvent.CreatedAt.IsZero() {
		t.Error("legacy row replayed with a zero CreatedAt — a history reader would " +
			"see year 1; the projection must stamp what arrives unstamped")
	}
	if !legacyEvent.CreatedAt.Equal(readInstant) {
		t.Errorf("legacy row CreatedAt = %v, want the read instant %v",
			legacyEvent.CreatedAt, readInstant)
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
