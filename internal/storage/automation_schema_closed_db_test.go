package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateBaseSchemaFailsOnClosedDB and TestMigrateAutomationRunsSchemaFailsOnClosedDB
// close the driver's own database connection first, so every db.Exec call
// inside the migration function fails identically - mirroring
// chat_session_admissions_test.go's "a closed database cannot even begin"
// precedent (applyContextMigration on a closed store.db). This exercises
// every db.Exec error-wrap branch these two migration functions have,
// closing the diff-coverage gap this refactor (splitting
// OpenSQLiteWithOptions to stay under its file's per-function LOC cap)
// otherwise left open.
func closedRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "closed-migration.db")))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}
	return db
}

func TestMigrateBaseSchemaFailsOnClosedDB(t *testing.T) {
	db := closedRawDB(t)
	if err := migrateBaseSchema(db); err == nil {
		t.Fatal("migrateBaseSchema succeeded on a closed database")
	}
}

func TestMigrateAutomationRunsSchemaFailsOnClosedDB(t *testing.T) {
	db := closedRawDB(t)
	if err := migrateAutomationRunsSchema(db); err == nil {
		t.Fatal("migrateAutomationRunsSchema succeeded on a closed database")
	}
}

// TestMigrateAutomationRunsSchemaFailsOnIndexNameCollision reaches the
// second db.Exec (CREATE INDEX) error branch specifically: SQLite indexes
// and tables share one namespace, so pre-creating a TABLE named exactly
// like the automation_runs index forces CREATE INDEX to fail with the
// table create (line above) already having succeeded - unlike the
// closed-DB test above, which fails at the first Exec and never reaches
// this second error wrap.
func TestMigrateAutomationRunsSchemaFailsOnIndexNameCollision(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "index-collision.db")))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE automation_runs_automation_id_idx (x INTEGER)`); err != nil {
		t.Fatalf("pre-create colliding table: %v", err)
	}
	if err := migrateAutomationRunsSchema(db); err == nil {
		t.Fatal("migrateAutomationRunsSchema succeeded despite an index/table name collision")
	}
}

// TestOpenSQLiteFailsWhenAutomationRunsIndexNameCollides reaches
// OpenSQLiteWithOptions' OWN error-wrap branch around
// migrateAutomationRunsSchema (sqlite.go, "if err :=
// migrateAutomationRunsSchema(db); err != nil { db.Close(); return nil,
// err }") - not just migrateAutomationRunsSchema's own internal error
// path, which TestMigrateAutomationRunsSchemaFailsOnIndexNameCollision
// above already covers by calling the function directly. The database
// FILE is pre-populated (via a raw sql.Open/Close, before
// OpenSQLiteWithOptions ever touches it) with a colliding table name, so
// the real OpenSQLiteWithOptions call path - not an isolated unit call -
// is what fails and reaches this specific line.
func TestOpenSQLiteFailsWhenAutomationRunsIndexNameCollides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-collision.db")
	seed, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE automation_runs_automation_id_idx (x INTEGER)`); err != nil {
		t.Fatalf("pre-create colliding table: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	if _, err := OpenSQLite(path); err == nil {
		t.Fatal("OpenSQLite succeeded despite a pre-existing automation_runs index/table name collision")
	}
}

// TestMigrateBaseSchemaFailsOnRunClaimsFenceAlter reaches
// migrateBaseSchema's SECOND ALTER TABLE error branch specifically (the
// fence_generation column) by pre-seeding run_claims as a VIEW, not a
// table, before migrateBaseSchema ever runs: CREATE TABLE IF NOT EXISTS
// is a no-op against an existing view of the same name (SQLite's
// sqlite_master name check does not distinguish object kind for IF NOT
// EXISTS), so the base-table loop (the FIRST error branch, already
// covered by TestMigrateBaseSchemaFailsOnClosedDB) succeeds, and the
// FIRST ALTER TABLE (the plain "fence" column) then fails with a real,
// non-"duplicate column" SQLite error (ALTER TABLE against a view), which
// this test asserts is NOT silently swallowed by the "duplicate column"
// tolerance the function's fence-add branch has for its own idempotent
// re-run case.
func TestMigrateBaseSchemaFailsOnRunClaimsFenceAlter(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "view-collision.db")))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Seed every OTHER base table for real, then substitute run_claims
	// with a view so the loop's own run_claims CREATE (IF NOT EXISTS)
	// no-ops against it and the subsequent ALTER TABLE targets a view,
	// not a table.
	for _, q := range []string{
		`CREATE TABLE events (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(run_id, sequence))`,
		`CREATE TABLE content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE spool_grants (ref TEXT NOT NULL, principal TEXT NOT NULL, PRIMARY KEY (ref, principal))`,
		`CREATE TABLE run_claims_backing (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL, fence INTEGER NOT NULL DEFAULT 1)`,
		`CREATE VIEW run_claims AS SELECT * FROM run_claims_backing`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	err = migrateBaseSchema(db)
	if err == nil {
		t.Fatal("migrateBaseSchema succeeded with run_claims substituted by a view")
	}
	if strings.Contains(err.Error(), "duplicate column") {
		t.Fatalf("error = %v, want a real ALTER-on-a-view failure, not the tolerated duplicate-column case", err)
	}
}

// TestOpenSQLiteFailsWhenBaseSchemaCannotMigrate reaches
// OpenSQLiteWithOptions' OWN error-wrap branch around migrateBaseSchema
// (sqlite.go, "if err := migrateBaseSchema(db); err != nil { db.Close();
// return nil, err }"), the same distinction
// TestOpenSQLiteFailsWhenAutomationRunsIndexNameCollides draws for its
// sibling migration call: the database FILE is pre-populated with the
// view substitution before OpenSQLiteWithOptions ever opens it, so the
// real production call path - not an isolated unit call to
// migrateBaseSchema - is what fails and reaches this line.
func TestOpenSQLiteFailsWhenBaseSchemaCannotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-view-collision.db")
	seed, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE run_claims_backing (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL, fence INTEGER NOT NULL DEFAULT 1)`,
		`CREATE VIEW run_claims AS SELECT * FROM run_claims_backing`,
	} {
		if _, err := seed.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	if _, err := OpenSQLite(path); err == nil {
		t.Fatal("OpenSQLite succeeded despite run_claims being a pre-existing view, not a table")
	}
}

// TestMigrateBaseSchemaFailsOnFencedTokensCreate reaches migrateBaseSchema's
// FINAL error branch (the fenced_tokens CREATE TABLE) specifically:
// SQLite indexes and tables share one namespace (the same trick
// TestMigrateAutomationRunsSchemaFailsOnIndexNameCollision uses above),
// so pre-creating an INDEX named exactly "fenced_tokens" makes CREATE
// TABLE IF NOT EXISTS fenced_tokens fail with a real name collision,
// while every earlier statement in the function (the four base tables,
// both ALTER TABLEs against a genuine run_claims table) succeeds
// normally first.
func TestMigrateBaseSchemaFailsOnFencedTokensCreate(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "fenced-tokens-collision.db")))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE backing_for_index (x INTEGER)`); err != nil {
		t.Fatalf("seed backing table: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX fenced_tokens ON backing_for_index(x)`); err != nil {
		t.Fatalf("seed colliding index: %v", err)
	}
	err = migrateBaseSchema(db)
	if err == nil {
		t.Fatal("migrateBaseSchema succeeded despite a pre-existing fenced_tokens index")
	}
	if !strings.Contains(err.Error(), "create fenced_tokens") {
		t.Fatalf("error = %v, want it naming the fenced_tokens create failure", err)
	}
}
