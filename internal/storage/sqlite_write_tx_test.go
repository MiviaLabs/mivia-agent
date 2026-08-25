package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSQLiteWriteDSNTakesImmediateTxLock pins the one property that separates
// the write pool from the read pool: _txlock=immediate, so BeginTx emits
// BEGIN IMMEDIATE and takes the write lock before the first read.
func TestSQLiteWriteDSNTakesImmediateTxLock(t *testing.T) {
	got := sqliteWriteDSN("/tmp/ctx.db")
	if !strings.Contains(got, "_txlock=immediate") {
		t.Fatalf("sqliteWriteDSN(%q) = %q, want _txlock=immediate", "/tmp/ctx.db", got)
	}
	if !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Fatalf("sqliteWriteDSN dropped the shared pragmas: %q", got)
	}
}

// TestSQLiteWriteDSNEscapesQuestionMarkPath keeps the write DSN under the same
// path-confusion guard as sqliteDSN: a '?' in the filename must be escaped
// into a file: URI, never left to split the DSN.
func TestSQLiteWriteDSNEscapesQuestionMarkPath(t *testing.T) {
	got := sqliteWriteDSN("/tmp/led?ger.db")
	if !strings.HasPrefix(got, "file:") {
		t.Fatalf("sqliteWriteDSN(%q) = %q, want a file: URI", "/tmp/led?ger.db", got)
	}
	if strings.Contains(got, "led?ger") {
		t.Fatalf("sqliteWriteDSN left a literal '?' in the path: %q", got)
	}
	if !strings.Contains(got, "_txlock=immediate") {
		t.Fatalf("escaped sqliteWriteDSN dropped _txlock: %q", got)
	}
}

// TestBeginWriteSurvivesConcurrentCommit is the regression this change exists
// for. Two stores share one file, as two mivia processes on one workspace do.
// A read-then-write transaction on the write pool must not lose its commit to
// an unrelated sibling commit that lands between its read and its write.
//
// With BEGIN DEFERRED the sibling commit wins the race and the write upgrade
// fails with SQLITE_BUSY_SNAPSHOT, which busy_timeout cannot clear. With
// BEGIN IMMEDIATE the transaction already holds the write lock, so the sibling
// blocks on busy_timeout and both commits land.
func TestBeginWriteSurvivesConcurrentCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context.db")
	first, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	sibling, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()

	ctx := context.Background()
	tx, err := first.beginWrite(ctx)
	if err != nil {
		t.Fatalf("beginWrite: %v", err)
	}
	// Read first, so a deferred transaction would hold only a read lock here.
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read inside write transaction: %v", err)
	}

	// The observable difference between the two txlock modes: under
	// BEGIN IMMEDIATE this transaction already holds the write lock, so the
	// sibling must block here. Under BEGIN DEFERRED it holds only a read
	// lock, the sibling commits at once, and this transaction's later write
	// upgrade is the one that fails.
	siblingDone := make(chan error, 1)
	go func() {
		siblingDone <- sibling.Append(ctx, Event{ID: "ev-sibling", RunID: "run-b", Sequence: 1, Kind: "k", Payload: []byte(`{"a":1}`)})
	}()
	select {
	case err := <-siblingDone:
		_ = tx.Rollback()
		t.Fatalf("sibling commit landed while a write transaction was open (err=%v); the write lock was not taken at BEGIN", err)
	case <-time.After(250 * time.Millisecond):
		// Blocked on the write lock, as BEGIN IMMEDIATE requires. The
		// sibling waits out busy_timeout=5000ms, well beyond this pause.
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`,
		"ev-first", "run-a", 1, "k", []byte(`{"a":1}`)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("write after read lost the race: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after concurrent sibling write: %v", err)
	}

	select {
	case err := <-siblingDone:
		if err != nil {
			t.Fatalf("sibling append after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sibling append never completed")
	}

	rows, err := first.db.QueryContext(ctx, `SELECT COUNT(*) FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no count row")
	}
	var total int
	if err := rows.Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("events = %d, want 2 (both commits durable)", total)
	}
}
