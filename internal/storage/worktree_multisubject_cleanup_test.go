package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestDeleteWorktreeSessionsAllowsExactLateSubjectCleanup(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner := mustCleanupPrincipal(t, "owner-session", "owner-subject")
	retained := mustCleanupPrincipal(t, "retained-session", "retained-subject")
	replacementOwner := mustCleanupPrincipal(t, "replacement-session", "retained-subject")
	old := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	replacement := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_2222222222222222"}
	oldPath := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	if err := registerCleanupInstance(ctx, store, owner, old, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, retained, "old-snapshot", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: oldPath, Worktree: old.Worktree, WorktreeInstance: old}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeSessionAdmission(ctx, retained, "old-snapshot", contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}, old); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: retained, Binding: mustBinding(t), WorktreeInstance: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO context_payloads(ref,namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data) VALUES(?,?,?,?,?,?,?,?,?,0,NULL)`,
		"ctxp_old", contextstate.Namespace, retained.WorkspaceID, retained.SessionID, retained.SubjectID, "digest", 1, "metadata", string(contextstate.RetentionSession),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(ctx, owner, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteWorktreeSessions(ctx, owner, old); err != nil {
		t.Fatal(err)
	}
	if err := registerCleanupInstance(ctx, store, owner, replacement, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, replacementOwner, "replacement-snapshot", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: oldPath, Worktree: replacement.Worktree, WorktreeInstance: replacement}); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteWorktreeSessions(ctx, retained, old)
	if err != nil || deleted != 2 {
		t.Fatalf("late exact cleanup = %d, %v; want 2, nil", deleted, err)
	}
	assertLateSubjectCleanup(t, store, owner, retained, replacementOwner, old, replacement, oldPath)
}

func assertLateSubjectCleanup(t *testing.T, store *SQLite, owner, retained, replacementOwner contextstate.Principal, old, replacement contextstate.WorktreeInstance, oldPath string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"chat_sessions", "chat_session_admissions", "chat_session_dirs", "worktree_catalog_keys", "worktree_routes"} {
		assertCleanupRowCount(t, store, table, retained, old.ID, 0)
	}
	var tombstoned, revoked, audits, tombstones int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=?`, retained.WorkspaceID, retained.SubjectID, retained.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT revoked FROM context_payloads WHERE ref='ctxp_old'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM context_audits WHERE workspace_id=? AND subject_id=? AND session_id=?`, retained.WorkspaceID, retained.SubjectID, retained.SessionID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM context_tombstones WHERE workspace_id=? AND subject_id=? AND session_id=?`, retained.WorkspaceID, retained.SubjectID, retained.SessionID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 1 || revoked != 1 || audits != 1 || tombstones != 1 {
		t.Fatalf("late lifecycle = tombstoned:%d revoked:%d audits:%d tombstones:%d", tombstoned, revoked, audits, tombstones)
	}
	assertCleanupRowCount(t, store, "chat_sessions", replacementOwner, replacement.ID, 1)
	if err := store.ValidateActiveWorktreeInstance(ctx, owner, replacement, oldPath); err != nil {
		t.Fatalf("replacement changed: %v", err)
	}
	if deleted, err := store.DeleteWorktreeSessions(ctx, retained, old); err != nil || deleted != 0 {
		t.Fatalf("repeated late cleanup = %d, %v; want 0, nil", deleted, err)
	}
	creatingPath := filepath.Join(t.TempDir(), "worktrees", "wt-creating")
	for _, invalid := range []contextstate.WorktreeInstance{
		replacement,
		{Worktree: "wt-creating", ID: "wt_3333333333333333"},
		{Worktree: "wt-missing", ID: "wt_4444444444444444"},
		{Worktree: "wt-wrong", ID: old.ID},
	} {
		if invalid.Worktree == "wt-creating" {
			if err := store.BeginWorktreeCreation(ctx, owner, invalid, creatingPath); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.DeleteWorktreeSessions(ctx, retained, invalid); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
			t.Fatalf("cleanup state for %+v = %v, want ErrWorktreeDeleted", invalid, err)
		}
	}
}

func mustCleanupPrincipal(t *testing.T, session, subject string) contextstate.Principal {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", session, subject)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func registerCleanupInstance(ctx context.Context, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, path string) error {
	if err := store.BeginWorktreeCreation(ctx, principal, instance, path); err != nil {
		return err
	}
	return store.RegisterWorktreeInstance(ctx, principal, instance, path)
}

func assertCleanupRowCount(t *testing.T, store *SQLite, table string, principal contextstate.Principal, instanceID string, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instanceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s rows = %d, want %d", table, count, want)
	}
}
