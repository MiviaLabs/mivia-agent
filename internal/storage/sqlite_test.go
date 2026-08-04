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
	for _, v := range []int{1, 2, 3, 4, 5, 6} {
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
		"chat_sessions", "chat_session_admissions", "chat_session_dirs", "worktree_routes",
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

// TestSQLiteGrantSpool covers the durable spool-grant surface: the
// empty-ref/empty-principal guard is a no-op (no row, no error) and
// INSERT OR IGNORE keeps re-granting the same (ref, principal) pair
// idempotent, so a re-spooled remainder never duplicates its grant row.
func TestSQLiteGrantSpool(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Guard no-op: an empty ref or principal must not error or write a row.
	if err := s.GrantSpool(ctx, "", "session-a"); err != nil {
		t.Fatalf("GrantSpool with empty ref: %v, want nil no-op", err)
	}
	if err := s.GrantSpool(ctx, "ref:output:guard", ""); err != nil {
		t.Fatalf("GrantSpool with empty principal: %v, want nil no-op", err)
	}

	const ref = "ref:output:durable"
	const principal = "session-a"
	if err := s.GrantSpool(ctx, ref, principal); err != nil {
		t.Fatalf("GrantSpool: %v", err)
	}
	// INSERT OR IGNORE: re-granting the same pair succeeds and keeps one row.
	if err := s.GrantSpool(ctx, ref, principal); err != nil {
		t.Fatalf("GrantSpool re-grant: %v, want idempotent success", err)
	}

	granted, err := s.CheckSpoolGrant(ctx, ref, principal)
	if err != nil {
		t.Fatalf("CheckSpoolGrant: %v", err)
	}
	if !granted {
		t.Fatal("CheckSpoolGrant = false after GrantSpool, want true")
	}
	if granted, err := s.CheckSpoolGrant(ctx, "ref:output:guard", ""); err != nil || granted {
		t.Fatalf("CheckSpoolGrant for guard-no-op pair = %v, %v; want false, nil", granted, err)
	}

	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM spool_grants`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("spool_grants rows = %d, want 1 (INSERT OR IGNORE must be idempotent)", rows)
	}
}
