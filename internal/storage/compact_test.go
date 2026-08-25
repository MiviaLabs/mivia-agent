package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func storePragma(t *testing.T, store *SQLite, name string) int64 {
	t.Helper()
	var v int64
	if err := store.db.QueryRowContext(context.Background(), `PRAGMA `+name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// TestCompactAdoptsTargetPageSizeAndIncrementalVacuum pins the rewrite's two
// durable effects. Both settings can only be adopted by a VACUUM: page_size
// is fixed at file creation, and auto_vacuum needs a full rebuild to add the
// pointer map. Neither can be changed by a PRAGMA alone on a populated file.
func TestCompactAdoptsTargetPageSizeAndIncrementalVacuum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := store.Append(ctx, Event{ID: "ev-" + itoa(i), RunID: "run", Sequence: i + 1, Kind: "agent", Payload: []byte(`{"body":"payload"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := storePragma(t, store, "page_size"); got != 4096 {
		t.Fatalf("page_size before compact = %d, want the 4096 default", got)
	}

	if err := store.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if got := storePragma(t, store, "page_size"); got != compactPageSize {
		t.Fatalf("page_size after compact = %d, want %d", got, compactPageSize)
	}
	// 2 is SQLITE_AUTO_VACUUM_INCREMENTAL.
	if got := storePragma(t, store, "auto_vacuum"); got != 2 {
		t.Fatalf("auto_vacuum after compact = %d, want 2 (incremental)", got)
	}
	// WAL must be restored: page_size can only change outside WAL mode, so
	// Compact drops to DELETE and must put the store back.
	var mode string
	if err := store.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode after compact = %q, want wal", mode)
	}
}

// TestCompactPreservesEveryRow is the safety half: a rewrite that loses data
// is worse than a large file.
func TestCompactPreservesEveryRow(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	const total = 120
	for i := 0; i < total; i++ {
		if err := store.Append(ctx, Event{ID: "ev-" + itoa(i), RunID: "run", Sequence: i + 1, Kind: "agent", Payload: []byte(`{"body":"payload"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after, err := store.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != total || after != before {
		t.Fatalf("rows before=%d after=%d, want %d both", before, after, total)
	}
	var integrity string
	if err := store.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q after compact", integrity)
	}
}

// TestCompactIsIdempotent: a second run finds the settings already adopted and
// must still succeed and still leave a WAL store.
func TestCompactIsIdempotent(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(ctx); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if got := storePragma(t, store, "page_size"); got != compactPageSize {
		t.Fatalf("page_size = %d after two compacts, want %d", got, compactPageSize)
	}
}

// TestCompactStoreStaysUsable proves the store still writes after the rewrite,
// so a compact is not a one-way trip.
func TestCompactStoreStaysUsable(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, Event{ID: "post", RunID: "run", Sequence: 1, Kind: "agent", Payload: []byte(`{"a":1}`)}); err != nil {
		t.Fatalf("append after compact: %v", err)
	}
	n, err := store.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count after post-compact append = %d, want 1", n)
	}
}
