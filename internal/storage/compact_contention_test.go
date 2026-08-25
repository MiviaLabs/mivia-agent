package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCompactLeaveWALRetriesTransientBusy proves the leave-WAL step tolerates
// a competitor whose connection pool closes within the retry budget
// (sqliteBusyRetryDelays, 150ms+400ms): previously this step failed at the
// very first SQLITE_BUSY with no retry, even though the failure is a proven
// no-op (nothing has been rewritten yet, so retrying costs nothing).
//
// Any other open *storage.SQLite on the same file blocks
// PRAGMA journal_mode=DELETE unconditionally while it exists - independent of
// whether it holds an active transaction - and only clears once fully
// closed; this is standard SQLite WAL behaviour (confirmed empirically
// against modernc.org/sqlite before writing this test), not a property of
// how this package uses it.
func TestCompactLeaveWALRetriesTransientBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	competitor, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = competitor.Close()
		close(closed)
	}()
	defer func() { <-closed }()

	if err := store.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v, want it to survive the competitor closing mid-retry", err)
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode after compact = %q, want wal", mode)
	}
}

// TestCompactRestoreWALReportsStuckDeleteModeOnFailure exercises
// compactRestoreWAL directly rather than through the full Compact rewrite.
// Re-entering WAL mode (unlike leaving it) could not be made to fail under
// any realistic contention this test tried - an open competitor transaction,
// PRAGMA locking_mode=EXCLUSIVE, an untouched sibling pool - so a synthetic
// full-Compact "stuck" scenario would be unreproducible and dishonest. A
// closed connection is a real failure class instead (matches what a lost
// disk, an unmounted volume, or a driver-level crash would produce), and it
// deterministically exercises the message-wrapping this test is actually
// about: when restore ever does fail, for any reason, the error must say so
// plainly rather than leaving an opaque joined error.
func TestCompactRestoreWALReportsStuckDeleteModeOnFailure(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	err = compactRestoreWAL(context.Background(), conn)
	if err == nil {
		t.Fatal("compactRestoreWAL succeeded against a closed connection")
	}
	if !strings.Contains(err.Error(), "DELETE") || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("compactRestoreWAL error = %q, want it to name the stuck DELETE mode and the manual recovery step", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("compactRestoreWAL error does not wrap the underlying cause")
	}
}
