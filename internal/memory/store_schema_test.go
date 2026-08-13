package memory

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// legacyMemorySchema is the pre-Wave-1 schema (no tier column), used to prove
// t1a's migration path: an existing DB created before this change must gain
// the tier column in place, without losing rows.
const legacyMemorySchema = `
CREATE TABLE IF NOT EXISTS memories (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral',
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memories_scope_org ON memories(scope, org);
`

func TestStoreSchemaFreshDBHasTierColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")
	db, _, err := openMemoryDB(path, false)
	if err != nil {
		t.Fatalf("openMemoryDB: %v", err)
	}
	defer db.Close()

	tier := columnDefault(t, db, "tier")
	if tier != "archive" {
		t.Fatalf("tier default = %q, want %q", tier, "archive")
	}
}

func TestStoreSchemaMigratesLegacyDBInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")

	// Hand-craft a pre-Wave-1 DB: legacy schema, no tier column, one row.
	legacy, err := sql.Open("sqlite", sqliteMemoryDSN(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := legacy.Exec(legacyMemorySchema); err != nil {
		t.Fatalf("legacy schema exec: %v", err)
	}
	if _, err := legacy.Exec(
		"INSERT INTO memories(id,scope,org,title,summary,verdict,tags,created,content) VALUES(?,?,?,?,?,?,?,?,?)",
		"legacy-id", "project", "", "legacy title", "legacy summary", "good", "", "2026-01-01", "legacy content",
	); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("legacy close: %v", err)
	}

	// Reopen through the real path: this must migrate the legacy DB in place.
	db, _, err := openMemoryDB(path, false)
	if err != nil {
		t.Fatalf("openMemoryDB (migration): %v", err)
	}
	defer db.Close()

	var tier string
	if err := db.QueryRow("SELECT tier FROM memories WHERE id = ?", "legacy-id").Scan(&tier); err != nil {
		t.Fatalf("select tier after migration: %v", err)
	}
	if tier != "archive" {
		t.Fatalf("migrated row tier = %q, want %q", tier, "archive")
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
		t.Fatalf("select count: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count after migration = %d, want 1 (no data loss)", count)
	}
}

// columnDefault reads the default clause modernc.org/sqlite reports for a
// column via PRAGMA table_info, and asserts the column exists at all.
func columnDefault(t *testing.T, db *sql.DB, column string) string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(memories)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == column {
			// PRAGMA table_info reports string literal defaults with their
			// surrounding SQL quotes, e.g. 'archive'; strip them for comparison.
			return strings.Trim(dflt.String, "'")
		}
	}
	t.Fatalf("column %q not found in memories table", column)
	return ""
}
