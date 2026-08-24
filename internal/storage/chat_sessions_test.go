package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := catalog.BeginWorktreeCreation(context.Background(), principal, instance, worktreeDir); err != nil {
		t.Fatalf("BeginWorktreeCreation: %v", err)
	}
	if err := catalog.RegisterWorktreeInstance(context.Background(), principal, instance, worktreeDir); err != nil {
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
	infos, err := catalog.ListWorktreeSessions(context.Background(), principal, instance)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.WorktreeInstance != instance {
			t.Fatalf("picker instance = %+v, want %+v", info.WorktreeInstance, instance)
		}
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

func TestDeleteWorktreeSessionsDeletesOnlyExactCatalogKeys(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	target, err := contextstate.NewPrincipal("workspace", "target-session", "target-subject")
	if err != nil {
		t.Fatal(err)
	}
	otherSubject, err := contextstate.NewPrincipal("workspace", "other-session", "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := contextstate.NewPrincipal("other-workspace", "other-workspace-session", "target-subject")
	if err != nil {
		t.Fatal(err)
	}
	targetInstance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	otherInstance := contextstate.WorktreeInstance{Worktree: "wt-b", ID: "wt_fedcba0987654321"}
	targetPath := filepath.Join(t.TempDir(), "worktrees", targetInstance.Worktree)
	otherInstancePath := filepath.Join(t.TempDir(), "worktrees", otherInstance.Worktree)
	otherWorkspacePath := filepath.Join(t.TempDir(), "worktrees", targetInstance.Worktree)
	registrations := []struct {
		principal contextstate.Principal
		instance  contextstate.WorktreeInstance
		path      string
	}{
		{target, targetInstance, targetPath},
		{target, otherInstance, otherInstancePath},
		{otherWorkspace, targetInstance, otherWorkspacePath},
	}
	for _, registration := range registrations {
		if err := store.BeginWorktreeCreation(ctx, registration.principal, registration.instance, registration.path); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterWorktreeInstance(ctx, registration.principal, registration.instance, registration.path); err != nil {
			t.Fatal(err)
		}
	}
	saves := []struct {
		principal contextstate.Principal
		instance  contextstate.WorktreeInstance
		name      string
	}{
		{target, targetInstance, "target-one"},
		{target, targetInstance, "target-two"},
		{otherSubject, targetInstance, "other-subject"},
		{target, otherInstance, "other-instance"},
		{otherWorkspace, targetInstance, "other-workspace"},
	}
	for _, save := range saves {
		if err := store.SaveSession(ctx, save.principal, save.name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: save.instance.Worktree, WorktreeInstance: save.instance}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.BeginWorktreeDeletion(ctx, target, targetInstance); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteWorktreeSessions(ctx, target, targetInstance); err != nil {
		t.Fatal(err)
	}
	assertExactCatalogKeyCleanup(t, store, target, otherSubject, otherWorkspace, targetInstance, otherInstance)
}

func assertExactCatalogKeyCleanup(t *testing.T, store *SQLite, target, otherSubject, otherWorkspace contextstate.Principal, targetInstance, otherInstance contextstate.WorktreeInstance) {
	t.Helper()
	checks := []struct {
		name       string
		workspace  string
		subject    string
		instanceID string
		want       int
	}{
		{"target", target.WorkspaceID, target.SubjectID, targetInstance.ID, 0},
		{"other subject", otherSubject.WorkspaceID, otherSubject.SubjectID, targetInstance.ID, 1},
		{"other instance", target.WorkspaceID, target.SubjectID, otherInstance.ID, 1},
		{"other workspace", otherWorkspace.WorkspaceID, otherWorkspace.SubjectID, targetInstance.ID, 1},
	}
	for _, check := range checks {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=?`, check.workspace, check.subject, check.instanceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Errorf("%s catalog keys = %d, want %d", check.name, count, check.want)
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
	path := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
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

func TestWorktreeCatalogRejectsNonCanonicalPath(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	worktreePath := filepath.Join(root, "wt-a")
	if err := os.Mkdir(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	nonCanonical := worktreePath + "/../wt-a"
	err = store.BeginWorktreeCreation(context.Background(), principal, instance, nonCanonical)
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("BeginWorktreeCreation(%q) = %v, want ErrInvalidDTO", nonCanonical, err)
	}
}

func TestRegisterWorktreeInstancePreservesMatchingLegacyRoute(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt-a")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", path); err != nil {
		t.Fatalf("SaveWorktreeRoute: %v", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatalf("BeginWorktreeCreation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatalf("RegisterWorktreeInstance: %v", err)
	}
	var legacy, bound int
	if err := store.db.QueryRow(`SELECT count(*) FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, instance.Worktree).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.Worktree, instance.ID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if legacy != 1 || bound != 1 {
		t.Fatalf("route rows = legacy %d, bound %d; want one of each", legacy, bound)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range infos {
		found = found || info.WorktreeInstance == instance
	}
	if !found {
		t.Fatalf("route picker rows do not retain instance %+v: %+v", instance, infos)
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
		path := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
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
		path := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
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
		path := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
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

func TestPruneWorktreeSessionSnapshotsRollsBackAllNames(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt-a")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := store.SaveSession(context.Background(), principal, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorktreeSessionAdmission(context.Background(), principal, name, contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}, instance); err != nil {
			t.Fatal(err)
		}
	}
	err = store.PruneWorktreeSessionSnapshots(context.Background(), principal, []string{"first", "bad/name"}, instance)
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("PruneWorktreeSessionSnapshots = %v, want ErrInvalidDTO", err)
	}
	for _, name := range []string{"first", "second"} {
		if _, _, err := store.LoadWorktreeSession(context.Background(), principal, name, instance); err != nil {
			t.Fatalf("LoadWorktreeSession(%q) after rollback: %v", name, err)
		}
		record, err := store.LoadWorktreeSessionAdmission(context.Background(), principal, name, instance)
		if err != nil || len(record.Names) != 1 {
			t.Fatalf("LoadWorktreeSessionAdmission(%q) = %#v, %v", name, record, err)
		}
	}
}

func TestPruneWorktreeSessionSnapshotsDeletesOnlyExactCatalogKey(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), instance.Worktree)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prune", "keep"} {
		if err := store.SaveSession(context.Background(), principal, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneWorktreeSessionSnapshots(context.Background(), principal, []string{"prune"}, instance); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name string
		want int
	}{{name: "prune", want: 0}, {name: "keep", want: 1}} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name=?`, principal.WorkspaceID, principal.SubjectID, instance.ID, check.name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Errorf("catalog key %q count = %d, want %d", check.name, count, check.want)
		}
	}
}

func TestDeleteWorktreeSessionSnapshotRemovesAllSideTableRows(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, instance, path, deleteKey := seedDeleteWorktreeSnapshotPair(t, store)
	assertDeleteWorktreeSnapshotMissingIsNoop(t, store, principal, instance)
	if err := store.DeleteWorktreeSessionSnapshot(context.Background(), principal, "delete-me", instance); err != nil {
		t.Fatal(err)
	}
	assertDeleteWorktreeSnapshotClearsAllSideTables(t, store, principal, instance, path, deleteKey)
	assertDeleteWorktreeSnapshotResaveMintsFreshRows(t, store, principal, instance, path, deleteKey)
}

func seedDeleteWorktreeSnapshotPair(t *testing.T, store *SQLite) (contextstate.Principal, contextstate.WorktreeInstance, string, string) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	path := filepath.Join(t.TempDir(), instance.Worktree)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"delete-me", "keep"} {
		if err := store.SaveSession(context.Background(), principal, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: path, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorktreeSessionAdmission(context.Background(), principal, name, contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}, instance); err != nil {
			t.Fatal(err)
		}
	}
	var deleteKey string
	if err := store.db.QueryRow(`SELECT storage_key FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name='delete-me'`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&deleteKey); err != nil {
		t.Fatal(err)
	}
	return principal, instance, path, deleteKey
}

// assertDeleteWorktreeSnapshotMissingIsNoop covers the negative path: a name
// with no catalog key removes nothing and reports ErrSessionNotFound before
// any delete runs.
func assertDeleteWorktreeSnapshotMissingIsNoop(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance) {
	t.Helper()
	if err := store.DeleteWorktreeSessionSnapshot(context.Background(), principal, "missing", instance); !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("DeleteWorktreeSessionSnapshot(missing) = %v, want ErrSessionNotFound", err)
	}
	for _, name := range []string{"delete-me", "keep"} {
		if _, _, err := store.LoadWorktreeSession(context.Background(), principal, name, instance); err != nil {
			t.Fatalf("LoadWorktreeSession(%q) after negative path: %v", name, err)
		}
	}
}

// assertDeleteWorktreeSnapshotClearsAllSideTables covers the success path: the
// deleted snapshot leaves no rows in any of the four tables, while the sibling
// snapshot keeps its dir and admission.
func assertDeleteWorktreeSnapshotClearsAllSideTables(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, path, deleteKey string) {
	t.Helper()
	if _, _, err := store.LoadWorktreeSession(context.Background(), principal, "delete-me", instance); !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("LoadWorktreeSession(delete-me) after delete = %v, want ErrSessionNotFound", err)
	}
	if record, err := store.LoadWorktreeSessionAdmission(context.Background(), principal, "delete-me", instance); err != nil || len(record.Names) != 0 {
		t.Fatalf("LoadWorktreeSessionAdmission(delete-me) after delete = %#v, %v", record, err)
	}
	checks := []struct {
		table string
		where string
		args  []any
	}{
		{"chat_sessions", "name=? AND instance_id=?", []any{deleteKey, instance.ID}},
		{"chat_session_admissions", "name=? AND instance_id=?", []any{deleteKey, instance.ID}},
		{"chat_session_dirs", "name=? AND instance_id=?", []any{deleteKey, instance.ID}},
		{"worktree_catalog_keys", "name=? AND storage_key=? AND entity='snapshot'", []any{"delete-me", deleteKey}},
	}
	for _, check := range checks {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM `+check.table+` WHERE workspace_id=? AND subject_id=? AND `+check.where, append([]any{principal.WorkspaceID, principal.SubjectID}, check.args...)...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s retains %d rows for the deleted snapshot", check.table, count)
		}
	}
	if _, info, err := store.LoadWorktreeSession(context.Background(), principal, "keep", instance); err != nil || info.Dir != path || info.Worktree != instance.Worktree || info.WorktreeInstance != instance {
		t.Fatalf("LoadWorktreeSession(keep) = info %+v, err %v", info, err)
	}
	record, err := store.LoadWorktreeSessionAdmission(context.Background(), principal, "keep", instance)
	if err != nil || len(record.Names) != 1 || record.Names[0] != "tool" {
		t.Fatalf("LoadWorktreeSessionAdmission(keep) = %#v, %v", record, err)
	}
}

// assertDeleteWorktreeSnapshotResaveMintsFreshRows covers re-entry: re-saving
// the deleted name mints a fresh key and rebuilds its rows, so save/delete
// cycles stay bounded.
func assertDeleteWorktreeSnapshotResaveMintsFreshRows(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, path, deleteKey string) {
	t.Helper()
	if err := store.SaveSession(context.Background(), principal, "delete-me", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: path, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatal(err)
	}
	var freshKey string
	if err := store.db.QueryRow(`SELECT storage_key FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name='delete-me'`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&freshKey); err != nil {
		t.Fatal(err)
	}
	if freshKey == deleteKey {
		t.Fatalf("re-save reused storage key %q, want a fresh key after delete", freshKey)
	}
	for _, table := range []string{"chat_sessions", "chat_session_dirs", "worktree_catalog_keys"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Errorf("%s row count after re-save = %d, want 2", table, count)
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

// TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint locks in the
// regression a bug audit caught before ship: an earlier version of
// liveContextSessionSQL fell back to the newest complete checkpoint whenever
// active_checkpoint_id was NULL, on the mistaken premise that NULL only ever
// meant "pointer lost track of a still-valid checkpoint." /clear
// (Advance with ClearActive: true, context_store.go's advanceActiveCheckpoint)
// also sets active_checkpoint_id=NULL, deliberately, while bumping
// session_revision/durable_revision past whatever checkpoint used to be
// active - a fallback that ignores that distinction resurrects a
// conversation the user explicitly cleared on the very next resume. This
// pins the correct behavior: after a clear, resume must stay empty even
// though an older complete checkpoint still exists on disk.
func TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	commit := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "sensitive-pre-clear-content")
	if err := s.Commit(ctx, commit); err != nil {
		t.Fatalf("commit: %v", err)
	}
	clear := contextstate.AdvanceRequest{
		OperationID: "clear-1", Principal: principal, SessionID: principal.SessionID,
		Expected:        contextstate.Revision{Session: commit.NewSession, Durable: commit.NewDurable, Source: commit.NewSourceSequence},
		ExpectedBinding: binding, NewBinding: binding,
		NewSession: commit.NewSession + 1, NewDurable: commit.NewDurable + 1, NewSourceSequence: commit.NewSourceSequence,
		ClearActive: true, Reason: "clear",
	}
	if err := s.Advance(ctx, clear); err != nil {
		t.Fatalf("Advance (clear): %v", err)
	}
	payload, _, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if strings.Contains(string(payload), "sensitive-pre-clear-content") {
		t.Fatalf("payload = %s, want the cleared conversation to stay gone, not resurrected from the older checkpoint", payload)
	}
}

// TestLoadSession_PrefersSnapshotWithMoreMessagesThanCheckpoint verifies that a
// completed live checkpoint is authoritative over any snapshot in chat_sessions,
// even if the snapshot has more messages (e.g. following /clear or compaction).
func TestLoadSession_PrefersSnapshotWithMoreMessagesThanCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	// commitFirstMessageCheckpoint commits a 2-message checkpoint (user+assistant).
	commitFirstMessageCheckpoint(t, s, principal, binding, "hello")
	// The snapshot reflects an older/stale or pre-clear/pre-compaction state with more messages.
	snapshotPayload := []byte(`[{"role":"user","content":"hello"},{"role":"assistant","content":"ok"},{"role":"user","content":"extra snapshot message"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, binding.Model, binding.Provider, 2, 1, 3, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !bytes.Contains(payload, []byte("hello")) || bytes.Contains(payload, []byte("extra snapshot message")) {
		t.Fatalf("payload = %s, want live checkpoint payload, not stale snapshot", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want the live identity %q preserved for takeover", info.SessionID, principal.SessionID)
	}
}

// TestLoadSession_KeepsCheckpointWhenSnapshotHasNoMoreMessages verifies that
// a same-or-fewer-message snapshot also never shadows the live checkpoint.
func TestLoadSession_KeepsCheckpointWhenSnapshotHasNoMoreMessages(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, s, principal, binding, "hello")
	snapshotPayload := []byte(`[{"role":"user","content":"stale snapshot"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !bytes.Contains(payload, []byte("hello")) {
		t.Fatalf("payload = %s, want the checkpoint payload (contains \"hello\"), not the smaller snapshot", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
}

// TestLiveContextSession_NoCompleteCheckpointServesEmptyPayload is the
// regression guard: with no complete checkpoint at all, resume must still
// serve the empty-context default exactly as before F1.
func TestLiveContextSession_NoCompleteCheckpointServesEmptyPayload(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
	if string(payload) != string(emptyContextPayload) {
		t.Fatalf("payload = %s, want emptyContextPayload", payload)
	}
}
