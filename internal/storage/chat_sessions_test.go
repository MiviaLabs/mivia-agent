package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestWorktreeSessionCatalogFencesDeletingInstance(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	catalog, ok := any(store).(contextstate.WorktreeSessionCatalog)
	if !ok {
		t.Fatal("SQLite does not implement WorktreeSessionCatalog")
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := catalog.BeginWorktreeCreation(context.Background(), principal, instance, "/repo/.mivia/worktrees/wt-a"); err != nil {
		t.Fatalf("BeginWorktreeCreation: %v", err)
	}
	if err := catalog.RegisterWorktreeInstance(context.Background(), principal, instance, "/repo/.mivia/worktrees/wt-a"); err != nil {
		t.Fatalf("RegisterWorktreeInstance: %v", err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t), WorktreeInstance: instance}); err != nil {
		t.Fatalf("EnsureSession active instance: %v", err)
	}
	var storedID sql.NullString
	if err := store.db.QueryRow(`SELECT instance_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID).Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if !storedID.Valid || storedID.String != instance.ID {
		t.Fatalf("stored instance ID = %q, want %q", storedID.String, instance.ID)
	}
	if err := store.SaveSession(context.Background(), principal, "snapshot", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := catalog.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatalf("BeginWorktreeDeletion: %v", err)
	}
	if _, err := store.LoadWorktree(context.Background(), principal, principal.SessionID, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("LoadWorktree during deletion = %v, want ErrWorktreeDeleted", err)
	}
	if err := store.SaveSession(context.Background(), principal, "late", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("SaveSession during deletion = %v, want ErrWorktreeDeleted", err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t), WorktreeInstance: instance}); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("EnsureSession during deletion = %v, want ErrWorktreeDeleted", err)
	}
	if deleted, err := catalog.DeleteWorktreeSessions(context.Background(), principal, instance); err != nil || deleted != 2 {
		t.Fatalf("DeleteWorktreeSessions = %d, %v; want 2, nil", deleted, err)
	}
	for _, table := range []string{"chat_sessions", "chat_session_dirs"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retains %d deleted rows", table, count)
		}
	}
}

func TestWorktreeCreationLifecycleActivatesOnlyItsCreatingInstance(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	path := "/repo/.mivia/worktrees/wt-a"
	if _, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, principal.WorkspaceID, instance.Worktree, instance.ID, path, contextstate.WorktreeCreating); err != nil {
		t.Fatalf("seed creating instance: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatalf("RegisterWorktreeInstance: %v", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, instance.Worktree, instance.ID).Scan(&state); err != nil {
		t.Fatalf("select worktree instance: %v", err)
	}
	if state != string(contextstate.WorktreeActive) {
		t.Fatalf("state = %q, want %q", state, contextstate.WorktreeActive)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("second RegisterWorktreeInstance = %v, want ErrWorktreeDeleted", err)
	}
}

func TestLoadWorktreeRejectsAnotherActiveInstance(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	first := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	second := contextstate.WorktreeInstance{Worktree: "wt-b", ID: "wt_fedcba0987654321"}
	for _, instance := range []contextstate.WorktreeInstance{first, second} {
		path := "/repo/.mivia/worktrees/" + instance.Worktree
		if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t), WorktreeInstance: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWorktree(context.Background(), principal, principal.SessionID, second); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("LoadWorktree with another instance = %v, want ErrWorktreeDeleted", err)
	}
}

func TestWorktreeSnapshotsWithSameNameRemainSeparate(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instances := []contextstate.WorktreeInstance{{Worktree: "wt-a", ID: "wt_1234567890abcdef"}, {Worktree: "wt-b", ID: "wt_fedcba0987654321"}}
	for _, instance := range instances {
		path := "/repo/.mivia/worktrees/" + instance.Worktree
		if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSession(context.Background(), principal, "same", []byte(`[{"role":"user"}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
			t.Fatal(err)
		}
	}
	for _, instance := range instances {
		data, _, err := store.LoadWorktreeSession(context.Background(), principal, "same", instance)
		if err != nil || string(data) != `[{"role":"user"}]` {
			t.Fatalf("LoadWorktreeSession(%s) = %q, %v", instance.Worktree, data, err)
		}
		infos, err := store.ListWorktreeSessions(context.Background(), principal, instance)
		if err != nil || len(infos) != 1 || infos[0].Name != "same" {
			t.Fatalf("ListWorktreeSessions(%s) = %#v, %v", instance.Worktree, infos, err)
		}
	}
}

func TestWorktreeAdmissionsWithSameNameRemainSeparate(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instances := []contextstate.WorktreeInstance{{Worktree: "wt-a", ID: "wt_1234567890abcdef"}, {Worktree: "wt-b", ID: "wt_fedcba0987654321"}}
	for index, instance := range instances {
		path := "/repo/.mivia/worktrees/" + instance.Worktree
		if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorktreeSessionAdmission(context.Background(), principal, "same", contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{fmt.Sprintf("tool-%d", index)}}, instance); err != nil {
			t.Fatal(err)
		}
	}
	for index, instance := range instances {
		record, err := store.LoadWorktreeSessionAdmission(context.Background(), principal, "same", instance)
		if err != nil || len(record.Names) != 1 || record.Names[0] != fmt.Sprintf("tool-%d", index) {
			t.Fatalf("LoadWorktreeSessionAdmission(%s) = %#v, %v", instance.Worktree, record, err)
		}
	}
}

func mustBinding(t *testing.T) contextstate.BindingRevision {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

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
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", "/repo/.mivia/worktrees/wt-a"); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "worktree:wt-a" || list[0].Dir != "/repo/.mivia/worktrees/wt-a" || list[0].Worktree != "wt-a" {
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

func TestSQLiteChatSessionCatalogHidesRouteWhenWorktreeHasSession(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	const worktreeDir = "/repo/.mivia/worktrees/wt-a"
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
	if len(list) != 1 || list[0].Name != "real-session" || list[0].WorktreeRoute {
		t.Fatalf("session list = %+v, want the real worktree session only", list)
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
