package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationV7AddsWorktreeInstanceContract(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema: %v", err)
	}
	for _, table := range []string{"worktree_instances"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}
	for _, table := range []string{"worktree_routes", "chat_session_dirs", "chat_sessions", "context_sessions", "chat_session_admissions"} {
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			t.Fatalf("table info %s: %v", table, err)
		}
		found := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, typ string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			found = found || name == "instance_id"
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("%s.instance_id is missing", table)
		}
	}
}

func TestMigrationV9AddsInstanceAwareRouteContract(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`PRAGMA table_info(worktree_routes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	primaryKey := 0
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &key); err != nil {
			t.Fatal(err)
		}
		if key > primaryKey {
			primaryKey = key
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if primaryKey != 4 {
		t.Fatalf("worktree_routes primary key fields = %d, want 4", primaryKey)
	}
	if !contextVersionTablePresent(db, 9) {
		t.Fatal("v9 contract witness is missing")
	}
	if _, err := db.Exec(`DROP INDEX worktree_routes_instance_idx`); err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("repair v9 index: %v", err)
	}
	var indexCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='worktree_routes_instance_idx'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatal("v9 route index is missing after repair")
	}
}

func TestMigrationV9PreservesLegacyRouteWithBoundRoute(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4, applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8} {
		if err := apply(db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES('workspace','subject','wt-a','/worktree','now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := applyContextSchemaV9(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES('workspace','subject','wt-a','/worktree','now','now','wt_1234567890abcdef')`); err != nil {
		t.Fatalf("insert bound route after v9: %v", err)
	}
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("reopen after v9: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM worktree_routes WHERE workspace_id='workspace' AND subject_id='subject' AND worktree='wt-a'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("route rows = %d, want legacy and bound routes", count)
	}
}

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

	// Repair unblocks the migration, which then runs forward to the current
	// schema version.
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
	// Repair finalizes v3, and the migration then runs forward to the current
	// version - the admission table is v4, so a store stalled mid-v3 does not
	// stop at 3.
	if version != currentContextSchemaVersion {
		t.Fatalf("version = %d, want %d", version, currentContextSchemaVersion)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 3`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0", dirty)
	}
}

// TestMigrationV3ApplyFailure simulates a store at version 3 whose v4
// migration cannot land: the migrations table refuses to record version 4, so
// the v4 apply phase fails on its version row after its DDL has run inside the
// apply transaction. migrateContextSchema must surface that apply error (the
// v3->v4 error branch) and the failed apply must have rolled back: user_version
// stays 3 and the v4 admission table must not exist.
func TestMigrationV3ApplyFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "v3_apply_failure.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)), CHECK(version < 4))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}

	if err := migrateContextSchema(db); err == nil {
		t.Fatal("migrateContextSchema at v3 with failing v4 apply unexpectedly succeeded")
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("user_version = %d, want 3 after failed v4 apply", version)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_session_admissions'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("chat_session_admissions exists after rolled-back v4 apply")
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

// TestMigrationV11ConvergesFromEveryPriorVersion pins the v11 chain fix:
// fresh stores and stores at v8, v9, or v10 all land on v11, and v11's
// contract (chat_sessions.session_id TEXT nullable plus its witness table)
// is present in every case. Before the fix the v8 and v9 branches returned
// early at v10, so a store at either version could never reach v11.
func TestMigrationV11ConvergesFromEveryPriorVersion(t *testing.T) {
	apply := []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4, applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8, applyContextSchemaV9, applyContextSchemaV10}
	seed := func(t *testing.T, db *sql.DB, versions int) {
		t.Helper()
		if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < versions; i++ {
			if err := apply[i](db); err != nil {
				t.Fatal(err)
			}
		}
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, db *sql.DB)
	}{
		{name: "v0", prepare: func(t *testing.T, db *sql.DB) {}},
		{name: "v8", prepare: func(t *testing.T, db *sql.DB) { seed(t, db, 8) }},
		{name: "v9", prepare: func(t *testing.T, db *sql.DB) { seed(t, db, 9) }},
		{name: "v10", prepare: func(t *testing.T, db *sql.DB) { seed(t, db, 10) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			test.prepare(t, db)
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema from %s: %v", test.name, err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 11 {
				t.Fatalf("user_version = %d after %s, want 11", version, test.name)
			}
			found := false
			rows, err := db.Query(`PRAGMA table_info(chat_sessions)`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var cid, notNull, key int
				var name, typ string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &key); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if name == "session_id" {
					found = typ == "TEXT" && notNull == 0
				}
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatalf("chat_sessions.session_id (TEXT, nullable) is missing after %s", test.name)
			}
			if !contextVersionTablePresent(db, 11) {
				t.Fatalf("chat_sessions_v11_contract is missing after %s", test.name)
			}
		})
	}
}

// TestMigrationV11BackfillsProjectionSessionID seeds a v10 store whose
// chat_sessions rows are catalog projections and runs v11. The backfill must
// copy the live context session id into session_id for rows that project a
// live, instance-matched context session, and must leave every other row
// NULL: no live row (foo), a tombstoned row (Y), and a row bound to a
// different worktree instance than the live session it names (Z).
func TestMigrationV11BackfillsProjectionSessionID(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4, applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8, applyContextSchemaV9, applyContextSchemaV10} {
		if err := apply(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		sessionID  string
		tombstoned int
		instanceID any
	}{
		{sessionID: "X", tombstoned: 0, instanceID: nil},
		{sessionID: "bar", tombstoned: 0, instanceID: nil},
		{sessionID: "Y", tombstoned: 1, instanceID: nil},
		{sessionID: "Z", tombstoned: 0, instanceID: "wt_1111111111111111"},
	} {
		if _, err := db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned,instance_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "workspace", "subject", row.sessionID, "digest", 0, 0, 0, "provider", "model", 0, row.tombstoned, row.instanceID); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		name       string
		instanceID any
	}{
		{name: "X", instanceID: nil},
		{name: "foo", instanceID: nil},
		{name: "bar", instanceID: nil},
		{name: "Y", instanceID: nil},
		{name: "Z", instanceID: "wt_2222222222222222"},
	} {
		if _, err := db.Exec(`INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "workspace", "subject", row.name, "model", "provider", []byte("{}"), "now", "now", 0, 0, 0, row.instanceID); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema after v11 backfill seed: %v", err)
	}
	for _, want := range []struct {
		name       string
		sessionID  string
		hasSession bool
	}{
		{name: "X", sessionID: "X", hasSession: true},
		{name: "foo"},
		{name: "bar", sessionID: "bar", hasSession: true},
		{name: "Y"},
		{name: "Z"},
	} {
		var sessionID sql.NullString
		if err := db.QueryRow(`SELECT session_id FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=?`, "workspace", "subject", want.name).Scan(&sessionID); err != nil {
			t.Fatalf("read chat_sessions %s session_id: %v", want.name, err)
		}
		if sessionID.Valid != want.hasSession || (sessionID.Valid && sessionID.String != want.sessionID) {
			t.Fatalf("chat_sessions %s session_id = %v, want %q (valid=%v)", want.name, sessionID, want.sessionID, want.hasSession)
		}
	}
}
