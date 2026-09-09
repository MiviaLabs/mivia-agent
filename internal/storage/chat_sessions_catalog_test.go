package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestSQLiteChatSessionCatalogRoundTripAndPrune(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	catalog := contextstate.SessionCatalog(store)
	ctx := context.Background()
	if err := catalog.SaveSession(ctx, principal, "named", []byte(`[{}]`), "model", "provider", 1, 2, 1, contextstate.SessionSaveOptions{}); err != nil {
		t.Fatal(err)
	}
	data, info, err := catalog.LoadSession(ctx, principal, "named")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `[{}]` || info.Name != "named" || info.MessageCount != 1 {
		t.Fatalf("loaded catalog entry = %q, %+v", data, info)
	}
	list, err := catalog.ListSessions(ctx, principal)
	if err != nil || len(list) != 1 {
		t.Fatalf("listed sessions = %d, err=%v", len(list), err)
	}
	if err := catalog.PruneSessionSnapshots(ctx, principal, []string{"named"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.LoadSession(ctx, principal, "named"); err != contextstate.ErrSessionNotFound {
		t.Fatalf("load after prune error = %v", err)
	}
}

func TestSQLiteChatSessionCatalogDeleteTombstonesContextSession(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if err := contextstate.SessionCatalog(store).DeleteSessionSnapshot(context.Background(), principal, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	var tombstoned, audits, tombstones int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_audits WHERE session_id=?`, principal.SessionID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_tombstones WHERE session_id=?`, principal.SessionID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 1 || audits != 1 || tombstones != 1 {
		t.Fatalf("delete lifecycle = tombstoned:%d audits:%d tombstones:%d", tombstoned, audits, tombstones)
	}
}

// TestSQLiteChatSessionCatalogDeleteFindsSnapshotByStoredSessionID covers a
// named snapshot whose stored session_id column differs from its catalog
// name (the identity the desktop app's "delete" action is keyed on - see
// AgentSessionSummary.session_id in mivia-agent-desktop). DeleteSessionSnapshot
// must still find and remove the row (and its chat_session_dirs projection
// row) when looked up by that session_id, not only by name: falling through
// to the context-session tombstone path instead - which only matches
// context_sessions.session_id, never chat_sessions.name - would silently
// no-op, leaving the snapshot (and its project/dir association) orphaned
// forever.
func TestSQLiteChatSessionCatalogDeleteFindsSnapshotByStoredSessionID(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	const name = "snapshot-name"
	const divergedSessionID = "original-live-session-id"
	if err := store.SaveSession(context.Background(), principal, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: "/tmp/project"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	// Simulate a row whose session_id column diverges from its name - e.g.
	// legacy data from before name and session_id were required to match.
	if _, err := store.db.Exec(`UPDATE chat_sessions SET session_id=? WHERE workspace_id=? AND subject_id=? AND name=?`, divergedSessionID, principal.WorkspaceID, principal.SubjectID, name); err != nil {
		t.Fatal(err)
	}

	if err := contextstate.SessionCatalog(store).DeleteSessionSnapshot(context.Background(), principal, divergedSessionID); err != nil {
		t.Fatalf("DeleteSessionSnapshot(session_id) = %v, want nil", err)
	}

	for _, table := range []string{"chat_sessions", "chat_session_dirs"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retains %d rows for %q after delete-by-session_id", table, count, name)
		}
	}
}

func TestSQLiteChatSessionCatalogListsWorktreeRoutesOnlyForTheirOwner(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	other, err := contextstate.NewPrincipal("other-workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", worktreeDir); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "worktree:wt-a" || list[0].Dir != worktreeDir || list[0].Worktree != "wt-a" {
		t.Fatalf("worktree route list = %+v, want its one route", list)
	}
	otherList, err := store.ListSessions(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherList) != 0 {
		t.Fatalf("foreign workspace routes leaked: %+v", otherList)
	}
}

func TestSQLiteChatSessionCatalogHidesDeletedBoundRoute(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := contextstate.NewPrincipal("workspace", "owner", "owner-subject")
	if err != nil {
		t.Fatal(err)
	}
	other, err := contextstate.NewPrincipal("workspace", "other", "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt-a")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), owner, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), owner, instance, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,?)`, owner.WorkspaceID, other.SubjectID, instance.Worktree, path, instance.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), owner, instance); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteWorktreeSessions(context.Background(), owner, instance); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListSessions(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("deleted bound route remains visible: %+v", list)
	}
}

// TestSQLiteChatSessionCatalogKeepsRouteNextToInstancelessSession pins the
// listing contract the resume-routing gate depends on: a saved session with
// bare worktree metadata and no managed instance resumes PLAIN, so the
// route pseudo-row must stay visible next to it - it is the only affordance
// that starts an instance-scoped session in that worktree. (Before the
// routing gate existed, both rows dispatched identically and the route row
// was hidden as a duplicate.)
func TestSQLiteChatSessionCatalogKeepsRouteNextToInstancelessSession(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "wt-a")
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), principal, "real-session", []byte(`[{}]`), "model", "provider", 2, 3, 5, contextstate.SessionSaveOptions{Dir: worktreeDir, Worktree: "wt-a"}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	var haveSession, haveRoute bool
	for _, info := range list {
		if info.Name == "real-session" && !info.WorktreeRoute {
			haveSession = true
		}
		if info.WorktreeRoute && info.Worktree == "wt-a" {
			haveRoute = true
		}
	}
	if len(list) != 2 || !haveSession || !haveRoute {
		t.Fatalf("session list = %+v, want the instance-less session AND the route row", list)
	}
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "real-session"); err != nil {
		t.Fatal(err)
	}
	list, err = store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].WorktreeRoute {
		t.Fatalf("session list after deletion = %+v, want the worktree route", list)
	}
}
