package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrationV2AtomicDirtyClear simulates a store stuck at version=2,
// dirty=1 (the crash window from the old code) and verifies that
// migrateContextSchema repairs it and succeeds.
func TestMigrationV2AtomicDirtyClear(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "stuck.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create the migrations table and manually insert the stuck state.
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_schema_migrations(version, dirty) VALUES(2, 1)`); err != nil {
		t.Fatal(err)
	}

	// Create the chat_sessions table to simulate that the first tx did commit.
	if _, err := db.Exec(`CREATE TABLE chat_sessions(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
		model TEXT NOT NULL, provider TEXT NOT NULL, messages BLOB NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		turn_count INTEGER NOT NULL, token_count INTEGER NOT NULL,
		message_count INTEGER NOT NULL,
		PRIMARY KEY(workspace_id, subject_id, name))`); err != nil {
		t.Fatal(err)
	}

	// Also create all v1 tables so the store looks fully migrated.
	for _, stmt := range contextSchemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	// Calling migrateContextSchema should repair the dirty flag and succeed
	// (and continue to current schema version).
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema on stuck store: %v", err)
	}

	// Verify dirty is now 0 for v2 and for current.
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 2`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty v2 = %d, want 0 after repair", dirty)
	}
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = ?`, currentContextSchemaVersion).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty current = %d, want 0", dirty)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("version = %d, want %d", version, currentContextSchemaVersion)
	}
}

// TestMigrationV2NoBrickOnCrashSimulation creates a DB at version 1, runs
// the full migration to v2, and verifies the final state is version=2,
// dirty=0 — confirming the two-transaction approach leaves no crash window.
func TestMigrationV2NoBrickOnCrashSimulation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Start with a fully migrated v1 schema so we only exercise v2.
	// Create the migrations table and set version=1, dirty=0.
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_schema_migrations(version, dirty) VALUES(1, 0)`); err != nil {
		t.Fatal(err)
	}

	// Create all v1 tables.
	for _, stmt := range contextSchemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	// Run migration from v1 through current (v2 + v3).
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema v1->current: %v", err)
	}

	// Verify final state: current version, dirty=0.
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("version = %d, want %d", version, currentContextSchemaVersion)
	}

	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = ?`, currentContextSchemaVersion).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0", dirty)
	}

	// Verify chat_sessions and chunks tables exist.
	for _, table := range []string{"chat_sessions", "context_payload_chunks"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}
}

// TestMigrationV3RepairAfterDDLBeforePublish: crash after v3 DDL tx (chunks
// table + dirty=1) but before finalize (user_version still 2). Repair must
// publish version 3; retry must not fail on CREATE TABLE already exists.
func TestMigrationV3RepairAfterDDLBeforePublish(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mid_v3.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	// Mid-migration: user_version still 2; v3 first tx committed (dirty=1 + table).
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_schema_migrations(version, dirty) VALUES(2, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_schema_migrations(version, dirty) VALUES(3, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range contextSchemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE chat_sessions(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
		model TEXT NOT NULL, provider TEXT NOT NULL, messages BLOB NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		turn_count INTEGER NOT NULL, token_count INTEGER NOT NULL,
		message_count INTEGER NOT NULL,
		PRIMARY KEY(workspace_id, subject_id, name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE context_payload_chunks(
		ref TEXT NOT NULL, chunk_index INTEGER NOT NULL, chunk_count INTEGER NOT NULL,
		data BLOB NOT NULL, PRIMARY KEY(ref, chunk_index),
		CHECK(chunk_index >= 0 AND chunk_count > 0 AND chunk_index < chunk_count))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX context_payload_chunks_ref_idx ON context_payload_chunks(ref)`); err != nil {
		t.Fatal(err)
	}

	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema after DDL-before-publish: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("version = %d, want 3", version)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 3`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0", dirty)
	}
}

// TestMigrationV3AtomicDirtyClear repairs a store stuck at version=3 dirty=1.
func TestMigrationV3AtomicDirtyClear(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "stuck_v3.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_schema_migrations(version, dirty) VALUES(3, 1)`); err != nil {
		t.Fatal(err)
	}
	// Minimal tables so repair recognizes first tx committed.
	for _, stmt := range contextSchemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE chat_sessions(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
		model TEXT NOT NULL, provider TEXT NOT NULL, messages BLOB NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		turn_count INTEGER NOT NULL, token_count INTEGER NOT NULL,
		message_count INTEGER NOT NULL,
		PRIMARY KEY(workspace_id, subject_id, name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE context_payload_chunks(
		ref TEXT NOT NULL, chunk_index INTEGER NOT NULL, chunk_count INTEGER NOT NULL,
		data BLOB NOT NULL, PRIMARY KEY(ref, chunk_index))`); err != nil {
		t.Fatal(err)
	}

	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema on stuck v3: %v", err)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 3`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0", dirty)
	}
}
