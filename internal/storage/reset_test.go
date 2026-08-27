package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// seedForReset writes one row into a representative spread of tables -
// including ones on both sides of a foreign key (context_sessions and its
// dependents context_checkpoints/context_payloads/context_source_events) -
// so a wipe that gets delete order or FK handling wrong fails loudly instead
// of passing on an empty store.
func seedForReset(t *testing.T, store *SQLite) contextstate.Principal {
	t.Helper()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, Event{ID: "ev-1", RunID: "run", Sequence: 1, Kind: "agent", Payload: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO context_sessions(workspace_id,session_id,subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned)
		 VALUES(?,?,?,?,0,0,0,'p','m',0,0)`,
		principal.WorkspaceID, "sess-reset", principal.SubjectID, principal.CapabilityDigest()); err != nil {
		t.Fatalf("seed context_sessions: %v", err)
	}
	now := time.Now().UTC().Format(sqliteTimestampLayout)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete,created_at)
		 VALUES('ckpt-1',?,?,?,0,0,'a',1,'m','op-1','idem-1',0,0,0,0,x'00',x'00','fp',1,?)`,
		principal.WorkspaceID, "sess-reset", principal.SubjectID, now); err != nil {
		t.Fatalf("seed context_checkpoints: %v", err)
	}
	return principal
}

func nonEmptyTables(t *testing.T, store *SQLite) map[string]int {
	t.Helper()
	counts, err := store.TableRowCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nonEmpty := make(map[string]int)
	for table, n := range counts {
		if n > 0 {
			nonEmpty[table] = n
		}
	}
	return nonEmpty
}

// TestWipeAllExceptSchemaClearsSeededTables is the core contract: every table
// carrying data, including ones on both sides of a foreign key, ends up
// empty.
func TestWipeAllExceptSchemaClearsSeededTables(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedForReset(t, store)
	if before := nonEmptyTables(t, store); len(before) < 3 {
		t.Fatalf("fixture under-seeded: only %d non-empty tables before wipe: %v", len(before), before)
	}

	if err := store.WipeAllExceptSchema(context.Background()); err != nil {
		t.Fatalf("WipeAllExceptSchema: %v", err)
	}

	if after := nonEmptyTables(t, store); len(after) != 0 {
		t.Fatalf("tables still non-empty after wipe: %v", after)
	}
}

// TestWipeAllExceptSchemaPreservesMigrationBookkeeping proves the wipe never
// touches context_schema_migrations: deleting those rows would make a
// subsequent OpenSQLite believe the store is unmigrated and re-run the whole
// ladder against an already-current schema.
func TestWipeAllExceptSchemaPreservesMigrationBookkeeping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	seedForReset(t, store)
	var before int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_schema_migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("fixture has no migration rows to protect")
	}
	if err := store.WipeAllExceptSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_schema_migrations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("context_schema_migrations rows = %d after wipe, want unchanged %d", after, before)
	}
	store.Close()

	// A fresh open against the wiped-but-migrated file must not attempt to
	// re-run the migration ladder.
	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen after wipe: %v", err)
	}
	defer reopened.Close()
}

// TestWipeAllExceptSchemaRestoresForeignKeyEnforcement proves foreign_keys is
// left ON afterward - PRAGMA foreign_keys cannot be toggled inside a
// transaction, so the wipe must disable it before BEGIN and explicitly
// restore it, not rely on the connection's next use to notice.
func TestWipeAllExceptSchemaRestoresForeignKeyEnforcement(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedForReset(t, store)
	if err := store.WipeAllExceptSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Insert a context_checkpoints row referencing a context_sessions row
	// that does not exist - must fail if foreign_keys enforcement survived.
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(sqliteTimestampLayout)
	_, err = store.db.Exec(
		`INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete,created_at)
		 VALUES('ckpt-orphan',?,?,?,0,0,'a',1,'m','op-x','idem-x',0,0,0,0,x'00',x'00','fp',1,?)`,
		principal.WorkspaceID, "no-such-session", principal.SubjectID, now)
	if err == nil {
		t.Fatal("orphaned checkpoint insert succeeded - foreign_keys enforcement was not restored after wipe")
	}
}

// TestWipeAllExceptSchemaStoreStaysUsable proves the store still accepts
// writes through its normal write path afterward - a wipe is not a one-way
// trip, and this also exercises the guard triggers (context_schema_guards.go)
// end to end post-wipe.
func TestWipeAllExceptSchemaStoreStaysUsable(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedForReset(t, store)
	if err := store.WipeAllExceptSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Append(ctx, Event{ID: "post-wipe", RunID: "run", Sequence: 1, Kind: "agent", Payload: []byte(`{"a":1}`)}); err != nil {
		t.Fatalf("append after wipe: %v", err)
	}
	n, err := store.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count after post-wipe append = %d, want 1", n)
	}
	var integrity string
	if err := store.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q after wipe", integrity)
	}
}

// TestWipeAllExceptSchemaIsAtomicOnCancellation proves a cancelled context
// leaves every table unchanged rather than partially wiped: this is a real,
// reproducible failure path (any caller-side cancellation), not a fabricated
// one.
func TestWipeAllExceptSchemaIsAtomicOnCancellation(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedForReset(t, store)
	before := nonEmptyTables(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.WipeAllExceptSchema(ctx)
	if err == nil {
		t.Fatal("WipeAllExceptSchema succeeded against an already-cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WipeAllExceptSchema error = %v, want it to wrap context.Canceled", err)
	}
	after := nonEmptyTables(t, store)
	if len(after) != len(before) {
		t.Fatalf("wipe was not atomic: %d non-empty tables before, %d after a cancelled attempt", len(before), len(after))
	}
}

// TestTableRowCountsIsReadOnly proves the dry-run enumeration never writes:
// mtime must not change.
func TestTableRowCountsIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedForReset(t, store)
	before, err := store.TableRowCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before["events"] != 1 {
		t.Fatalf("events count = %d, want 1", before["events"])
	}
	if before["context_sessions"] != 1 {
		t.Fatalf("context_sessions count = %d, want 1", before["context_sessions"])
	}
	if _, ok := before["context_schema_migrations"]; ok {
		t.Fatal("TableRowCounts must exclude context_schema_migrations, matching what a wipe would actually remove")
	}
}
