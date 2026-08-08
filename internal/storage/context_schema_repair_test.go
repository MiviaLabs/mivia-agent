package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedContextCrashState builds a store interrupted between migration v's apply
// phase and its finalize phase: every migration below v fully applied, v's
// tables committed, the (v,1) row committed, and user_version still v-1. The
// real migration functions produce the DDL so the seed cannot drift from it.
func seedContextCrashState(t *testing.T, v int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	apply := []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4, applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8, applyContextSchemaV9, applyContextSchemaV10}
	// A migration added without extending this list would silently go untested:
	// the loop below would panic for the new version, or worse, a caller passing
	// a lower v would still pass. Fail loudly instead.
	if len(apply) != currentContextSchemaVersion {
		t.Fatalf("seed covers %d migrations but the schema is at v%d - extend apply", len(apply), currentContextSchemaVersion)
	}
	for i := 0; i < v; i++ {
		if err := apply[i](db); err != nil {
			t.Fatalf("seed migration v%d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE context_schema_migrations SET dirty = 1 WHERE version = ?`, v); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestMigrateRecoversFromApplyPhaseCrash pins the only reachable interrupted
// state for every shipped migration. Before the repair re-drove the finalize
// phase, reopening such a store re-ran the migration and failed permanently on
// "table already exists".
func TestMigrateRecoversFromApplyPhaseCrash(t *testing.T) {
	for v := 1; v <= currentContextSchemaVersion; v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			db := seedContextCrashState(t, v)
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema after v%d crash: %v", v, err)
			}
			assertContextSchemaClean(t, db)
			// A second open must be a no-op, not a fresh failure.
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("reopen after repair: %v", err)
			}
			assertContextSchemaClean(t, db)
		})
	}
}

func TestSchemaRepairReplacesMalformedLiveWorktreeIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX worktree_instances_live_name_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX worktree_instances_live_name_idx ON worktree_instances(workspace_id,instance_id) WHERE state != 'deleted'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLite(path)
	if err != nil {
		t.Fatalf("repair malformed live-name index: %v", err)
	}
	defer store.Close()
	for index, id := range []string{"wt_1111111111111111", "wt_2222222222222222"} {
		_, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','same',?,?,'creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id, fmt.Sprintf("/tmp/wt-%d", index))
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && err == nil {
			t.Fatal("schema repair accepted two live instances with the same workspace and worktree")
		}
	}
}

func TestSchemaRepairPreservesLiveIndexPredicateLiteralCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP INDEX worktree_instances_live_name_idx`,
		`CREATE UNIQUE INDEX worktree_instances_live_name_idx ON worktree_instances(workspace_id,worktree) WHERE state != 'DELETED'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLite(path)
	if err != nil {
		t.Fatalf("repair predicate literal: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','same','wt_1111111111111111','/tmp/old','deleted',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','same','wt_2222222222222222','/tmp/new','creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
		store.Close()
		t.Fatalf("same-name recreation after index repair: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err != nil {
		t.Fatalf("idempotent reopen after predicate repair: %v", err)
	} else if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaRepairRejectsDuplicateLiveWorktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX worktree_instances_live_name_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX worktree_instances_live_name_idx ON worktree_instances(workspace_id,instance_id) WHERE state != 'deleted'`); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"wt_1111111111111111", "wt_2222222222222222"} {
		if _, err := db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','same',?,?,'creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id, fmt.Sprintf("/tmp/wt-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("schema repair accepted duplicate live worktrees")
	}
}

func TestSchemaRepairReplacesMalformedWorktreeInstanceIDIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX worktree_instances_id_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX worktree_instances_id_idx ON worktree_instances(workspace_id,worktree,instance_id)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLite(path)
	if err != nil {
		t.Fatalf("repair malformed instance-ID index: %v", err)
	}
	defer store.Close()
	for index, name := range []string{"wt-a", "wt-b"} {
		_, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace',?,'wt_1111111111111111',?,'creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, name, fmt.Sprintf("/tmp/wt-%d", index))
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && err == nil {
			t.Fatal("schema repair accepted one instance ID for two worktree names")
		}
	}
}

func TestSchemaRepairRejectsDuplicateWorktreeInstanceIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX worktree_instances_id_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX worktree_instances_id_idx ON worktree_instances(workspace_id,worktree,instance_id)`); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"wt-a", "wt-b"} {
		if _, err := db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace',?,'wt_1111111111111111',?,'creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, name, fmt.Sprintf("/tmp/wt-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("schema repair accepted duplicate worktree instance IDs")
	}
}

func TestSchemaRepairRebuildsMalformedWorktreeRoutesV9(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db := openMalformedWorktreeRoutesV9(t, path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLite(path)
	if err != nil {
		t.Fatalf("repair malformed v9 routes: %v", err)
	}
	defer store.Close()
	for index, dir := range []string{"/tmp/a", "/tmp/b"} {
		_, err := store.db.Exec(`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES('workspace','subject','wt-a',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'wt_1111111111111111')`, dir)
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && err == nil {
			t.Fatal("schema repair accepted a duplicate exact worktree route")
		}
	}
}

func TestSchemaRepairRejectsDuplicateExactWorktreeRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db := openMalformedWorktreeRoutesV9(t, path)
	for _, dir := range []string{"/tmp/a", "/tmp/b"} {
		if _, err := db.Exec(`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES('workspace','subject','wt-a',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'wt_1111111111111111')`, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("schema repair accepted duplicate exact worktree routes")
	}
}

func openMalformedWorktreeRoutesV9(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`ALTER TABLE worktree_routes RENAME TO worktree_routes_old`,
		`CREATE TABLE worktree_routes(workspace_id TEXT NOT NULL,subject_id TEXT NOT NULL,worktree TEXT NOT NULL,dir TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,instance_id TEXT,PRIMARY KEY(workspace_id,subject_id,worktree,dir))`,
		`INSERT INTO worktree_routes SELECT * FROM worktree_routes_old`,
		`DROP TABLE worktree_routes_old`,
		`CREATE UNIQUE INDEX worktree_routes_instance_idx ON worktree_routes(workspace_id,subject_id,worktree,dir)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func TestSchemaRepairReplacesMalformedV7SecondaryIndexes(t *testing.T) {
	tests := []struct {
		name string
		bad  string
		want string
	}{
		{name: "worktree_routes_worktree_instance_idx", bad: `CREATE UNIQUE INDEX worktree_routes_worktree_instance_idx ON worktree_routes(instance_id)`, want: `CREATE INDEX worktree_routes_worktree_instance_idx ON worktree_routes(worktree,instance_id)`},
		{name: "chat_session_dirs_worktree_instance_idx", bad: `CREATE UNIQUE INDEX chat_session_dirs_worktree_instance_idx ON chat_session_dirs(instance_id)`, want: `CREATE INDEX chat_session_dirs_worktree_instance_idx ON chat_session_dirs(worktree,instance_id)`},
		{name: "chat_sessions_instance_idx", bad: `CREATE UNIQUE INDEX chat_sessions_instance_idx ON chat_sessions(instance_id)`, want: `CREATE INDEX chat_sessions_instance_idx ON chat_sessions(instance_id)`},
		{name: "context_sessions_instance_idx", bad: `CREATE UNIQUE INDEX context_sessions_instance_idx ON context_sessions(instance_id)`, want: `CREATE INDEX context_sessions_instance_idx ON context_sessions(instance_id)`},
		{name: "chat_session_admissions_instance_idx", bad: `CREATE UNIQUE INDEX chat_session_admissions_instance_idx ON chat_session_admissions(instance_id)`, want: `CREATE INDEX chat_session_admissions_instance_idx ON chat_session_admissions(instance_id)`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "context.db")
			store, err := OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP INDEX IF EXISTS ` + test.name); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.bad); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var definition string
			if err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, test.name).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			if normalizeSchemaDefinition(definition) != normalizeSchemaDefinition(test.want) {
				t.Fatalf("index definition = %q, want %q", definition, test.want)
			}
		})
	}
}

func TestSchemaRepairRestoresWorktreeInstanceStateConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	seedWorktreeInstancesWithoutStateCheck(t, path, "active")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','bad','wt_2222222222222222','/tmp/bad','corrupt',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	if err == nil {
		t.Fatal("repaired worktree table accepted an invalid state")
	}
}

func TestSchemaRepairRejectsInvalidWorktreeInstanceState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	seedWorktreeInstancesWithoutStateCheck(t, path, "corrupt")
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("schema repair accepted an invalid worktree state")
	}
}

func TestSchemaRepairRejectsNonNullableLegacyInstanceColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`ALTER TABLE chat_session_dirs RENAME TO chat_session_dirs_old`,
		`CREATE TABLE chat_session_dirs(workspace_id TEXT NOT NULL,subject_id TEXT NOT NULL,name TEXT NOT NULL,dir TEXT NOT NULL DEFAULT '',worktree TEXT NOT NULL DEFAULT '',instance_id TEXT NOT NULL,PRIMARY KEY(workspace_id,subject_id,name))`,
		`INSERT INTO chat_session_dirs SELECT * FROM chat_session_dirs_old`,
		`DROP TABLE chat_session_dirs_old`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("schema repair accepted a non-null legacy instance column")
	}
}

func TestNewerSchemaFailsBeforeRepairMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`ALTER TABLE worktree_instances ADD COLUMN future_value TEXT`,
		`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at,future_value) VALUES('workspace','wt-a','wt_1111111111111111','/tmp/wt-a','active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'keep-me')`,
		`PRAGMA user_version = 11`,
		`UPDATE context_schema_migrations SET dirty=1 WHERE version=7`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("newer schema was accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT future_value FROM worktree_instances WHERE worktree='wt-a'`).Scan(&value); err != nil {
		t.Fatalf("rejected newer schema changed future column: %v", err)
	}
	if value != "keep-me" {
		t.Fatalf("future value = %q, want keep-me", value)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version=7`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 {
		t.Fatalf("newer schema dirty flag = %d, want unchanged", dirty)
	}
}

func TestNewerSchemaDoesNotCreateMigrationTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 11`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(path); err == nil {
		reopened.Close()
		t.Fatal("newer schema was accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='context_schema_migrations'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("newer schema gained context_schema_migrations")
	}
}

func seedWorktreeInstancesWithoutStateCheck(t *testing.T, path, state string) {
	t.Helper()
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`DROP TABLE worktree_instances`,
		`CREATE TABLE worktree_instances(workspace_id TEXT NOT NULL,worktree TEXT NOT NULL,instance_id TEXT NOT NULL,canonical_path TEXT NOT NULL,state TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(workspace_id,worktree,instance_id))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','wt-a','wt_1111111111111111','/tmp/wt-a',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, state); err != nil {
		t.Fatal(err)
	}
}

func assertContextSchemaClean(t *testing.T, db *sql.DB) {
	t.Helper()
	version, dirty, err := contextSchemaState(db)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, currentContextSchemaVersion)
	}
	if dirty {
		t.Fatal("schema still dirty after repair")
	}
	for _, table := range []string{"context_sessions", "chat_sessions", "chat_session_admissions", "chat_session_dirs", "worktree_routes", "worktree_instances", "worktree_catalog_keys", "worktree_routes_v9_contract"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s missing after repair", table)
		}
	}
}

// TestMigrateReportsDirtyWhenApplyPhaseNeverCommitted keeps the repair honest:
// a dirty row without its tables is not a finalize-phase interruption, so it
// must surface rather than be cleared.
func TestMigrateReportsDirtyWhenApplyPhaseNeverCommitted(t *testing.T) {
	db := seedContextCrashState(t, currentContextSchemaVersion)
	if _, err := db.Exec(`DROP TABLE ` + contextVersionTable(currentContextSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	err := migrateContextSchema(db)
	if err == nil {
		t.Fatal("migrateContextSchema must refuse a store whose apply phase never committed")
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = ?`, currentContextSchemaVersion).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 {
		t.Fatalf("dirty = %d, want the flag left set", dirty)
	}
}

// TestRepairStopsAtTheFirstUnrepairableVersion pins the loop's stop condition.
// A dirty row whose tables are absent is not a finalize-phase interruption, and
// no later version can be genuinely repairable behind it: the versions are
// applied in order, so a missing v1 means whatever a later witness proves was
// not produced by this migration sequence. Repair must therefore write nothing
// at all past that point - publishing a user_version over a store missing its
// v1 tables is a worse state than the one it found.
//
// This is a test gap rather than a live defect: reaching the state needs an
// externally dropped table, and both behaviours still fail the open closed on
// the dirty flag. What the guard buys is that the failed open is non-destructive.
func TestRepairStopsAtTheFirstUnrepairableVersion(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "partial.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	// v4's witness is present and its row is dirty, but v1 never landed.
	if err := applyContextSchemaV4(db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA user_version = 0`,
		`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(1, 1)`,
		`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(4, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	if err := repairContextSchema(db); err != nil {
		t.Fatalf("repair: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("user_version = %d; repair published a version over a store missing its v1 tables", version)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 4`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 {
		t.Fatal("repair cleared a later version's dirty flag after declining an earlier one")
	}
	if err := migrateContextSchema(db); err == nil {
		t.Fatal("a store whose v1 apply phase never committed was reported usable")
	}
}

// TestRepairNeverRewindsTheSchemaVersion pins the direction of travel:
// user_version only moves forward. A dirty row for a version the store is
// already past is a stale marker, and re-driving its finalize phase must clear
// the flag WITHOUT republishing the old version - a rewind would make the next
// open re-apply migrations whose tables already exist and fail forever.
//
// The state is currently unreachable in normal operation: a (v, dirty=1) row is
// only written by the apply phase of v, which runs only while the store reads
// as older than v, and the v1/v2 DDL is non-idempotent so a process that loses
// the race rolls back without leaving a row. The guard is defence in depth for
// the first idempotent migration that lands above an existing one, and the
// property is cheap enough to pin now.
func TestRepairNeverRewindsTheSchemaVersion(t *testing.T) {
	store, _ := admissionStore(t)
	if _, err := store.db.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("reopen with a stale dirty row for an old version: %v", err)
	}
	assertContextSchemaClean(t, store.db)

	// The same property stated directly at the seam, so a caller that passes a
	// current version above v is pinned even without the migrate wrapper.
	if _, err := store.db.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(1, 1)`); err != nil {
		t.Fatal(err)
	}
	repaired, err := finalizeContextVersion(store.db, 1, currentContextSchemaVersion)
	if err != nil || !repaired {
		t.Fatalf("finalizeContextVersion = (%v, %v), want (true, nil)", repaired, err)
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("user_version = %d after repairing an old version, want %d", version, currentContextSchemaVersion)
	}
}

func TestContextVersionTableHasNoWitnessForAnUnknownVersion(t *testing.T) {
	// An unknown version has no table whose presence proves its apply phase
	// committed, so repair must decline rather than guess.
	if got := contextVersionTable(99); got != "" {
		t.Fatalf("contextVersionTable(99) = %q, want no witness", got)
	}
	store, _ := admissionStore(t)
	repaired, err := finalizeContextVersion(store.db, 99, 0)
	if err != nil || repaired {
		t.Fatalf("finalizeContextVersion(99) = (%v, %v), want (false, nil)", repaired, err)
	}
}

func TestFinalizeContextVersionReportsFailures(t *testing.T) {
	store, _ := admissionStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER block_repair BEFORE UPDATE ON context_schema_migrations BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeContextVersion(store.db, currentContextSchemaVersion, currentContextSchemaVersion); err == nil {
		t.Fatal("repair reported success while the dirty clear was blocked")
	}
	// The blocked repair must also surface through migrateContextSchema.
	if _, err := store.db.Exec(`UPDATE context_schema_migrations SET dirty = 1 WHERE version = ?`, currentContextSchemaVersion); err != nil {
		// The trigger blocks the UPDATE, so drive the row in directly.
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(?, 1)`, currentContextSchemaVersion); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateContextSchema(store.db); err == nil {
		t.Fatal("migrate reported success while repair was blocked")
	}
	// An unreadable store has no witness, so repair declines rather than
	// reporting a failure it cannot act on.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if repaired, err := finalizeContextVersion(store.db, 3, 3); repaired || err != nil {
		t.Fatalf("finalizeContextVersion on a closed store = (%v, %v), want (false, nil)", repaired, err)
	}
	if contextVersionTablePresent(store.db, 3) {
		t.Fatal("a closed store reported a witness table")
	}
}

func TestRepairReportsAnUnreadableSchemaVersion(t *testing.T) {
	// Repair decides what to re-drive from the version it reads; without it
	// there is no safe default, so the failure must surface rather than be
	// treated as "nothing to repair".
	store, _ := admissionStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repairContextSchema(store.db); err == nil {
		t.Fatal("repair proceeded without reading the schema version")
	}
}
