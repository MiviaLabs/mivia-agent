package storage

// Live finding (2026-08-15): the workflow ledger's own content refs
// ("sha256:<hex>", minted by internal/workflows/ledger and
// internal/workflows/controller - see agent_step_errors.go, panel_types.go)
// are stored in the same `content` table the coordinator/subagent ledger
// uses ("ref:<kind>:<hex>", from internal/ledger/contentref), but
// AppendAndDeleteRun only deletes a run's `events` rows - it never touches
// `content`. A deleted run's output/error/diff blobs are orphaned forever:
// unreferenced by any live event, but never reclaimed. PruneOrphanedContent
// closes that gap, scoped ONLY to the "sha256:" prefix so it can never
// touch a live chat/subagent content row (a different prefix, same table).

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPruneOrphanedContentRemovesOnlyUnreferencedWorkflowRefs(t *testing.T) {
	withoutOrphanGrace(t)
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	liveRef := "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"[:64]
	orphanRef := "sha256:" + "2222222222222222222222222222222222222222222222222222222222222222"[:64]
	coordinatorRef := "ref:output:" + "3333333333333333333333333333333333333333333333333333333333333333"[:64]

	for _, ref := range []string{liveRef, orphanRef, coordinatorRef} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO content(ref, data) VALUES (?, ?)`, ref, []byte("payload for "+ref)); err != nil {
			t.Fatal(err)
		}
	}

	// A live run whose event payload references liveRef.
	if err := s.Append(ctx, Event{ID: "e1", RunID: "wfr-live", Sequence: 1, Kind: "step_completed",
		Payload: []byte(`{"output_ref":"` + liveRef + `"}`)}); err != nil {
		t.Fatal(err)
	}
	// A deleted run: AppendAndDeleteRun strips its real events down to a
	// tombstone, so orphanRef (which it used to reference) is no longer
	// reachable from any event payload.
	if err := s.Append(ctx, Event{ID: "e2", RunID: "wfr-deleted", Sequence: 1, Kind: "step_completed",
		Payload: []byte(`{"output_ref":"` + orphanRef + `"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAndDeleteRun(ctx, Event{ID: "e3", RunID: "wfr-deleted", Sequence: 2, Kind: "run_deleted",
		Payload: []byte(`{"run_id":"wfr-deleted"}`)}, Claim{}); err != nil {
		t.Fatal(err)
	}

	removed, err := s.PruneOrphanedContent(ctx)
	if err != nil {
		t.Fatalf("PruneOrphanedContent: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only orphanRef)", removed)
	}

	assertContentRow(t, s, liveRef, true, "referenced by a live run's event")
	assertContentRow(t, s, orphanRef, false, "referenced only by a deleted run's stripped event")
	assertContentRow(t, s, coordinatorRef, true, "a different ref prefix (coordinator/chat); must never be touched")
}

func assertContentRow(t *testing.T, s *SQLite, ref string, wantPresent bool, why string) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM content WHERE ref = ?`, ref).Scan(&n); err != nil {
		t.Fatal(err)
	}
	present := n == 1
	if present != wantPresent {
		t.Fatalf("content row %q present=%v, want %v (%s)", ref, present, wantPresent, why)
	}
}

func TestPruneOrphanedContentIsIdempotent(t *testing.T) {
	withoutOrphanGrace(t)
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	ref := "sha256:" + "4444444444444444444444444444444444444444444444444444444444444444"[:64]
	if _, err := s.db.ExecContext(ctx, `INSERT INTO content(ref, data) VALUES (?, ?)`, ref, []byte("x")); err != nil {
		t.Fatal(err)
	}
	first, err := s.PruneOrphanedContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("first pass removed = %d, want 1", first)
	}
	second, err := s.PruneOrphanedContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Fatalf("second pass removed = %d, want 0 (already pruned)", second)
	}
}

// withoutOrphanGrace disables the GC grace window for a test, so the scan
// logic can be asserted on freshly written blobs. Production keeps the window:
// it is what protects a blob whose naming event has not landed yet.
func withoutOrphanGrace(t *testing.T) {
	t.Helper()
	prev := orphanGraceInterval
	orphanGraceInterval = "-0 seconds"
	t.Cleanup(func() { orphanGraceInterval = prev })
}

// TestPruneOrphanedContentSparesRecentBlobs pins the grace window itself.
// Producers store content BEFORE appending the event that names it, so a blob
// written moments ago may simply be waiting for its event. Deleting it left a
// durable event whose output_ref resolved to ErrContentNotFound.
func TestPruneOrphanedContentSparesRecentBlobs(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref := "sha256:" + "3333333333333333333333333333333333333333333333333333333333333333"
	if err := store.PutContent(context.Background(), ref, []byte("just written")); err != nil {
		t.Fatalf("StoreContent: %v", err)
	}
	removed, err := store.PruneOrphanedContent(context.Background())
	if err != nil {
		t.Fatalf("PruneOrphanedContent: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0: a blob written seconds ago may still be awaiting its event", removed)
	}
	if _, err := store.GetContent(context.Background(), ref); err != nil {
		t.Fatalf("LoadContent after GC: %v", err)
	}
}
