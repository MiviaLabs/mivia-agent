package storage

import (
	"database/sql"
	"path/filepath"
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
