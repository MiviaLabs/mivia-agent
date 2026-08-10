package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// v4Store builds a store at schema v4 with no chat_session_dirs table: the
// state every existing user's context.db is in before the v5 migration.
// It opens the database directly because OpenSQLite migrates straight to
// currentContextSchemaVersion.
func v4Store(t *testing.T) (*SQLite, contextstate.Principal) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "v4.db")))
	if err != nil {
		t.Fatal(err)
	}
	store := &SQLite{db: db}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for i, fn := range []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4} {
		if err := fn(db); err != nil {
			t.Fatalf("seed migration v%d: %v", i+1, err)
		}
	}
	principal, err := contextstate.NewPrincipal("ws", "sess-1", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	return store, principal
}

// TestSessionDirsRoundTrip verifies that directory metadata written with a
// named snapshot comes back through LoadSession and ListSessions.
func TestSessionDirsRoundTrip(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate to v5: %v", err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	opts := contextstate.SessionSaveOptions{Dir: worktreeDir, Worktree: "wt-a"}
	if err := store.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1, opts); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	_, info, err := store.LoadSession(ctx, principal, "snap")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.Dir != opts.Dir || info.Worktree != opts.Worktree {
		t.Fatalf("LoadSession Dir/Worktree = %q/%q, want %q/%q", info.Dir, info.Worktree, opts.Dir, opts.Worktree)
	}
	list, err := store.ListSessions(ctx, principal)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || list[0].Dir != opts.Dir || list[0].Worktree != opts.Worktree {
		t.Fatalf("ListSessions = %+v, want one row with Dir/Worktree", list)
	}
}

// TestSessionDirsSurviveUpgrade is the upgrade regression: a v4 store holding
// existing snapshots must, after the v5 migration, still load and list them
// with Dir="" (no dir row exists for pre-upgrade rows), not fail with NULL
// scan errors.
func TestSessionDirsSurviveUpgrade(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	// Seed a snapshot the way v4 wrote it: no chat_session_dirs row.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SubjectID, "old", "m", "p", []byte(`[{"role":"user"}]`), "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z", 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate to v5: %v", err)
	}
	_, info, err := store.LoadSession(ctx, principal, "old")
	if err != nil {
		t.Fatalf("LoadSession of pre-upgrade row: %v", err)
	}
	if info.Dir != "" || info.Worktree != "" {
		t.Fatalf("pre-upgrade row Dir/Worktree = %q/%q, want empty", info.Dir, info.Worktree)
	}
	list, err := store.ListSessions(ctx, principal)
	if err != nil {
		t.Fatalf("ListSessions of pre-upgrade rows: %v", err)
	}
	if len(list) != 1 || list[0].Name != "old" {
		t.Fatalf("ListSessions = %+v, want the pre-upgrade row listed", list)
	}
}

// TestSessionDirsDeletedWithSnapshot verifies the dir row is reclaimed when
// its snapshot is deleted.
func TestSessionDirsDeletedWithSnapshot(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	opts := contextstate.SessionSaveOptions{Dir: "/x", Worktree: ""}
	if err := store.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1, opts); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSessionSnapshot(ctx, principal, "snap"); err != nil {
		t.Fatalf("DeleteSessionSnapshot: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, "snap").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dir row survived snapshot delete (%d rows)", count)
	}
}

// TestSessionDirsPrunedWithSnapshot verifies the dir row is reclaimed when a
// prune removes its snapshot.
func TestSessionDirsPrunedWithSnapshot(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	opts := contextstate.SessionSaveOptions{Dir: "/x", Worktree: ""}
	if err := store.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1, opts); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneSessionSnapshots(ctx, principal, []string{"snap", "ghost"}); err != nil {
		t.Fatalf("PruneSessionSnapshots: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, "snap").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dir row survived prune (%d rows)", count)
	}
}

// TestSessionDirsRejectInvalidMetadata verifies NUL bytes and oversized
// directory strings are rejected at the storage seam.
func TestSessionDirsRejectInvalidMetadata(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	bad := contextstate.SessionSaveOptions{Dir: "bad\x00dir"}
	if err := store.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1, bad); err == nil {
		t.Fatal("SaveSession accepted a NUL-containing dir")
	}
	huge := contextstate.SessionSaveOptions{Dir: strings.Repeat("x", contextstate.MaxSessionDirBytes+1)}
	if err := store.SaveSession(ctx, principal, "snap2", []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1, huge); err == nil {
		t.Fatal("SaveSession accepted an oversized dir")
	}
}

// TestEnsureSessionRecordsDir verifies a live context session's directory is
// recorded at EnsureSession time and returned through LoadSession.
func TestEnsureSessionRecordsDir(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	binding := contextstate.BindingRevision{Provider: "p", Model: "m", Generation: 1}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, Dir: worktreeDir, Worktree: "wt-a"}); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	_, info, err := store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession by session id: %v", err)
	}
	if info.Dir != worktreeDir || info.Worktree != "wt-a" {
		t.Fatalf("context session Dir/Worktree = %q/%q, want the recorded values", info.Dir, info.Worktree)
	}
}

// TestEnsureSessionDirSurfacesInListSessions is the BUG-1 regression: the
// live-session branch of ListSessions must surface the directory recorded by
// EnsureSession, or the picker never shows a marker and restore never fires
// for the default session surface.
func TestEnsureSessionDirSurfacesInListSessions(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	binding := contextstate.BindingRevision{Provider: "p", Model: "m", Generation: 1}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, Dir: worktreeDir, Worktree: "wt-a"}); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	// A session appears in the picker only after its first committed turn
	// (source_sequence > 0). Simulate that commit so the row is listed.
	if _, err := store.db.ExecContext(ctx, `UPDATE context_sessions SET source_sequence=1 WHERE workspace_id=? AND subject_id=? AND session_id=?`, principal.WorkspaceID, principal.SubjectID, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListSessions(ctx, principal)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSessions = %+v, want the live session listed", list)
	}
	if list[0].Dir != worktreeDir || list[0].Worktree != "wt-a" {
		t.Fatalf("live session Dir/Worktree = %q/%q, want the recorded values", list[0].Dir, list[0].Worktree)
	}
}

// TestContextTombstoneRemovesDirRow verifies deleting a context-backed session
// also reclaims its dir row.
func TestContextTombstoneRemovesDirRow(t *testing.T) {
	store, principal := v4Store(t)
	ctx := context.Background()
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	binding := contextstate.BindingRevision{Provider: "p", Model: "m", Generation: 1}
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, Dir: "/x", Worktree: ""}); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := store.DeleteSessionSnapshot(ctx, principal, principal.SessionID); err != nil {
		t.Fatalf("DeleteSessionSnapshot (context-backed): %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, principal.SessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dir row survived context session tombstone (%d rows)", count)
	}
}
