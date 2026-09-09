package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationV16AddsMemoryIndexSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := migrateContextSchema(db); err != nil {
		t.Fatalf("migrateContextSchema: %v", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 16 {
		t.Fatalf("user_version = %d, want 16", version)
	}
	for _, table := range []string{"memory_sources", "memory_entries"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}
	for _, index := range []string{"memory_entries_project_idx", "memory_entries_org_idx", "memory_sources_project_idx"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name); err != nil {
			t.Fatalf("%s index missing: %v", index, err)
		}
	}
}

func TestMigrationV16MemoryIndexIdentityIsComposite(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO memory_entries
		(id, scope, project_id, org_id, source_path, source_hash, title, summary, content, indexed_at)
		VALUES ('same', 'project', 'project-a', '', 'a.md', 'hash-a', 'A', 'A', 'A', 'now')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_entries
		(id, scope, project_id, org_id, source_path, source_hash, title, summary, content, indexed_at)
		VALUES ('same', 'project', 'project-b', '', 'b.md', 'hash-b', 'B', 'B', 'B', 'now')`); err != nil {
		t.Fatalf("same memory id in two projects must be allowed: %v", err)
	}
}

func TestEnsureContextSchemaV16RejectsMalformedExistingIndexTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memory_entries(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureContextSchemaV16(db); err == nil {
		t.Fatal("ensureContextSchemaV16 accepted a malformed memory_entries table")
	}
}

// TestApplyContextSchemaV16PropagatesMigrationFailure drives
// applyContextSchemaV16 directly (it is unexported, same package) against a
// bare connection that never ran migrateContextSchema. The DDL statements
// land fine, but the apply phase's final
// `INSERT OR REPLACE INTO context_schema_migrations` fails for real because
// that bookkeeping table does not exist yet - exercising the
// applyContextMigration error-propagation branch.
func TestApplyContextSchemaV16PropagatesMigrationFailure(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyContextSchemaV16(db); err == nil {
		t.Fatal("applyContextSchemaV16 accepted a database with no context_schema_migrations table")
	}
}

// TestValidateMemoryIndexSchemaPropagatesTableInfoQueryError closes the
// connection before calling validateMemoryIndexSchema directly, so the very
// first `PRAGMA table_info(...)` query fails with a real driver error
// ("sql: database is closed") instead of a fabricated one.
func TestValidateMemoryIndexSchemaPropagatesTableInfoQueryError(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateMemoryIndexSchema(db); err == nil {
		t.Fatal("validateMemoryIndexSchema accepted a closed database")
	}
}

// TestValidateMemoryIndexSchemaRejectsTableMissingColumn rebuilds
// memory_entries without its "tier" column, keeping the indexes intact so
// the missing-column check (not the earlier index check) is what fires.
func TestValidateMemoryIndexSchemaRejectsTableMissingColumn(t *testing.T) {
	db := newMigratedSchemaV16DB(t)
	if _, err := db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memoryEntriesTableMissingTierColumnSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS memory_entries_project_idx ON memory_entries(scope, project_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS memory_entries_org_idx ON memory_entries(scope, org_id)`); err != nil {
		t.Fatal(err)
	}
	err := validateMemoryIndexSchema(db)
	if err == nil {
		t.Fatal("validateMemoryIndexSchema accepted a memory_entries table missing the tier column")
	}
	if !strings.Contains(err.Error(), "missing column tier") {
		t.Fatalf("error = %v, want it to name the missing tier column", err)
	}
}

// TestValidateMemoryIndexSchemaRejectsMissingIndex drops one of the three
// required indexes and expects the index-presence check (not the earlier
// column check, which still passes) to report it by name.
func TestValidateMemoryIndexSchemaRejectsMissingIndex(t *testing.T) {
	db := newMigratedSchemaV16DB(t)
	if _, err := db.Exec(`DROP INDEX memory_entries_project_idx`); err != nil {
		t.Fatal(err)
	}
	err := validateMemoryIndexSchema(db)
	if err == nil {
		t.Fatal("validateMemoryIndexSchema accepted a database missing memory_entries_project_idx")
	}
	if !strings.Contains(err.Error(), "memory_entries_project_idx") {
		t.Fatalf("error = %v, want it to name the missing index", err)
	}
}

// TestValidateMemoryIndexSchemaRejectsAlteredCheckConstraint rebuilds
// memory_entries with every column and index intact but a reordered verdict
// CHECK clause, so its literal SQL text no longer contains the exact
// constraint validateMemoryIndexSchema requires - reaching the constraint
// check (not the column or index checks, both of which still pass).
func TestValidateMemoryIndexSchemaRejectsAlteredCheckConstraint(t *testing.T) {
	db := newMigratedSchemaV16DB(t)
	if _, err := db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memoryEntriesTableReorderedVerdictCheckSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS memory_entries_project_idx ON memory_entries(scope, project_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS memory_entries_org_idx ON memory_entries(scope, org_id)`); err != nil {
		t.Fatal(err)
	}
	err := validateMemoryIndexSchema(db)
	if err == nil {
		t.Fatal("validateMemoryIndexSchema accepted a memory_entries table with an altered verdict CHECK clause")
	}
	if !strings.Contains(err.Error(), "missing constraint") {
		t.Fatalf("error = %v, want it to report a missing constraint", err)
	}
}

// newMigratedSchemaV16DB opens a fresh SQLite database, runs the full
// migration chain, and registers cleanup. Shared by the
// validateMemoryIndexSchema rejection tests above.
func newMigratedSchemaV16DB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// memoryEntriesTableMissingTierColumnSQL is the real memory_entries DDL
// from memoryIndexSchemaStatements with the "tier" column deleted; every
// other column, default, and CHECK constraint is unchanged.
const memoryEntriesTableMissingTierColumnSQL = `CREATE TABLE memory_entries (
  id TEXT NOT NULL CHECK(id <> ''),
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL CHECK(source_path <> ''),
  source_hash TEXT NOT NULL CHECK(source_hash <> ''),
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral' CHECK(verdict IN ('good','bad','mixed','neutral')),
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK((scope='project' AND project_id <> '' AND org_id='') OR (scope='org' AND project_id='' AND org_id <> '')),
  PRIMARY KEY(id, scope, project_id, org_id)
)`

// memoryEntriesTableReorderedVerdictCheckSQL is the real memory_entries DDL
// with every column present (including tier) but the verdict CHECK clause's
// value order changed, so the literal substring
// validateMemoryIndexSchema looks for is no longer present.
const memoryEntriesTableReorderedVerdictCheckSQL = `CREATE TABLE memory_entries (
  id TEXT NOT NULL CHECK(id <> ''),
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL CHECK(source_path <> ''),
  source_hash TEXT NOT NULL CHECK(source_hash <> ''),
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral' CHECK(verdict IN ('bad','good','mixed','neutral')),
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  tier TEXT NOT NULL DEFAULT 'archive' CHECK(tier IN ('core','archive')),
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK((scope='project' AND project_id <> '' AND org_id='') OR (scope='org' AND project_id='' AND org_id <> '')),
  PRIMARY KEY(id, scope, project_id, org_id)
)`
