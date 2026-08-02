package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestContextSchemaMigration(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentContextSchemaVersion)
	}
	var dirty int
	for _, v := range []int{1, 2, 3} {
		if err := s.db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = ?`, v).Scan(&dirty); err != nil {
			t.Fatalf("dirty for version %d: %v", v, err)
		}
		if dirty != 0 {
			t.Fatalf("migration dirty flag for version %d = %d, want 0", v, dirty)
		}
	}
	for _, table := range []string{
		"context_sessions", "context_source_events", "context_payloads",
		"context_payload_chunks",
		"context_checkpoints", "context_audits", "context_tombstones",
		"chat_sessions",
	} {
		var name string
		if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys on pooled connection = %d, want 1", foreignKeys)
	}
}
