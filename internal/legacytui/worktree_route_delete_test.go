package legacytui

import (
	"context"
	"database/sql"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	_ "modernc.org/sqlite"
)

// findWorktreeRoute returns the session-list row for one worktree route.
func findWorktreeRoute(t *testing.T, infos []chat.SessionInfo, name string) chat.SessionInfo {
	t.Helper()
	for _, info := range infos {
		if info.WorktreeRoute && info.Worktree == name {
			return info
		}
	}
	t.Fatalf("no worktree route row for %q in %+v", name, infos)
	return chat.SessionInfo{}
}

// sessionInfosFromCatalog converts catalog rows to the sidebar's session info
// shape, mapping the fields the route-deletion paths read.
func sessionInfosFromCatalog(infos []contextstate.SessionCatalogInfo) []chat.SessionInfo {
	out := make([]chat.SessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, chat.SessionInfo{
			SessionID: info.SessionID, Title: info.Title, Name: info.Name,
			Model: info.Model, Provider: info.Provider,
			Dir: info.Dir, Worktree: info.Worktree, WorktreeRoute: info.WorktreeRoute,
			WorktreeInstance: info.WorktreeInstance,
		})
	}
	return out
}

// readySidebarModel builds a TUI model whose sessions sidebar selects the
// first row and owns focus.
func readySidebarModel(t *testing.T, repoRoot string, infos []chat.SessionInfo) *TUIModel {
	t.Helper()
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.sessions = infos
	m.sessionsSidebar = newSessionsSidebar()
	m.sessionsSidebar.move(infos, 1)
	m.setFocus(cli.FocusSidebar)
	return m
}

// TestSidebarDeletesBrokenWorktreeRoute pins the fix for the reported stuck
// row: a worktree route whose marker is lost (instance still active) cannot
// be opened (ErrWorktreeDeleted, "worktree session deleted") and could not be
// deleted from the session list. The sidebar must arm deletion for a broken
// route and remove the worktree plus its rows on confirm.
func TestSidebarDeletesBrokenWorktreeRoute(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "stuck-route", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	// Lose the marker while the instance stays active.
	if err := os.Remove(cli.WorktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	route := findWorktreeRoute(t, sessionInfosFromCatalog(infos), worktree.Name)
	if route.WorktreeInstance.IsZero() {
		t.Fatalf("route row has no instance: %+v", route)
	}

	m := readySidebarModel(t, repoRoot, sessionInfosFromCatalog(infos))
	if err := m.openSessionInfo(route); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("open broken route = %v, want ErrWorktreeDeleted", err)
	}
	if !m.handleSidebarKey("d") {
		t.Fatal("d key was not handled")
	}
	if m.sessionsSidebar.confirm != confirmDeleteOne {
		t.Fatalf("confirm after d = %v, want confirmDeleteOne", m.sessionsSidebar.confirm)
	}
	m.applySidebarSessionsConfirm()
	for _, info := range m.sessions {
		if info.WorktreeRoute && info.Worktree == worktree.Name {
			t.Fatalf("route row still listed: %+v", info)
		}
	}
	if !strings.Contains(m.sessionsSidebar.notice, "deleted") {
		t.Fatalf("delete notice = %q", m.sessionsSidebar.notice)
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("resolve removed worktree = %v, %v", resolved, err)
	}
	if _, err := os.Stat(worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
}

// TestSidebarKeepsHealthyWorktreeRouteOnWorktreesNotice pins that a route
// whose worktree is openable keeps the /worktrees guidance: deletion of a
// healthy worktree stays a /worktrees action, never a session-list action.
func TestSidebarKeepsHealthyWorktreeRouteOnWorktreesNotice(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "healthy-route", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	route := findWorktreeRoute(t, sessionInfosFromCatalog(infos), worktree.Name)
	if route.WorktreeInstance.IsZero() {
		t.Fatalf("route row has no instance: %+v", route)
	}

	m := readySidebarModel(t, repoRoot, sessionInfosFromCatalog(infos))
	if err := m.openSessionInfo(route); err != nil {
		t.Fatalf("healthy route must open: %v", err)
	}
	if !m.handleSidebarKey("d") {
		t.Fatal("d key was not handled")
	}
	if m.sessionsSidebar.confirm != confirmNone {
		t.Fatalf("healthy route armed delete: %v", m.sessionsSidebar.confirm)
	}
	if !strings.Contains(m.sessionsSidebar.notice, "/worktrees") {
		t.Fatalf("notice = %q, want /worktrees guidance", m.sessionsSidebar.notice)
	}
	// A forced confirm must still refuse and keep the worktree.
	m.sessionsSidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved == nil {
		t.Fatalf("healthy worktree was removed: %v, %v", resolved, err)
	}
}

// TestSidebarDeletesLegacyGhostRoute pins that a legacy route row (no
// instance binding) whose worktree directory is gone can be deleted from the
// session list: cleanup removes the stale route so it stops reappearing.
func TestSidebarDeletesLegacyGhostRoute(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "legacy-ghost", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM worktree_instances WHERE worktree=?`, worktree.Name); err != nil {
		db.Close()
		store.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM worktree_routes WHERE worktree=? AND instance_id IS NOT NULL`, worktree.Name); err != nil {
		db.Close()
		store.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, worktree.Name, worktree.Path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	route := findWorktreeRoute(t, sessionInfosFromCatalog(infos), worktree.Name)
	if !route.WorktreeInstance.IsZero() {
		t.Fatalf("legacy route has an instance: %+v", route)
	}

	m := readySidebarModel(t, repoRoot, sessionInfosFromCatalog(infos))
	if !m.handleSidebarKey("d") {
		t.Fatal("d key was not handled")
	}
	if m.sessionsSidebar.confirm != confirmDeleteOne {
		t.Fatalf("confirm after d = %v, want confirmDeleteOne", m.sessionsSidebar.confirm)
	}
	m.applySidebarSessionsConfirm()
	for _, info := range m.sessions {
		if info.WorktreeRoute && info.Worktree == worktree.Name {
			t.Fatalf("route row still listed: %+v", info)
		}
	}
	store, err = cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err = store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.WorktreeRoute && info.Worktree == worktree.Name {
			t.Fatalf("stale route survived cleanup: %+v", info)
		}
	}
}

// TestSidebarRefusesStaleRouteDeletingReplacementWorktree pins the safety
// rule: deleting a stale route row whose instance was replaced must refuse
// and leave the replacement worktree untouched.
func TestSidebarRefusesStaleRouteDeletingReplacementWorktree(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "replaced", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	oldInstance, err := cli.BeginManagedWorktreeRemoval(repoRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cli.FinishManagedWorktreeRemoval(repoRoot, oldInstance); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.CreateManagedWorktree(repoRoot, "replaced", "HEAD", "mivia/"); err != nil {
		t.Fatal(err)
	}

	m := readySidebarModel(t, repoRoot, sessionInfosFromCatalog(infos))
	m.sessionsSidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()
	if !strings.Contains(m.sessionsSidebar.notice, "changed") {
		t.Fatalf("notice = %q, want a changed-worktree refusal", m.sessionsSidebar.notice)
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, "replaced")
	if err != nil || resolved == nil {
		t.Fatalf("replacement worktree was removed: %v, %v", resolved, err)
	}
}

// TestSidebarDeletesStaleInstanceBoundRouteWhenInstanceGone pins that a stale
// route row whose worktree was removed out-of-band (instance deleted, rows
// cleaned) is dropped from the list on confirm instead of failing with
// "worktree not found": the row is a stale snapshot, so deletion reports
// success and removes it from the sidebar.
func TestSidebarDeletesStaleInstanceBoundRouteWhenInstanceGone(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "gone-route", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := cli.BeginManagedWorktreeRemoval(repoRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cli.FinishManagedWorktreeRemoval(repoRoot, instance); err != nil {
		t.Fatal(err)
	}

	// The sidebar snapshot still holds the route row for the removed instance.
	stale := chat.SessionInfo{
		Name: "worktree:" + worktree.Name, Worktree: worktree.Name,
		Dir: worktree.Path, WorktreeRoute: true, WorktreeInstance: instance,
	}
	m := readySidebarModel(t, repoRoot, []chat.SessionInfo{stale})
	m.sessionsSidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()
	if len(m.sessions) != 0 {
		t.Fatalf("stale route row still listed: %+v", m.sessions)
	}
	if !strings.Contains(m.sessionsSidebar.notice, "deleted") {
		t.Fatalf("delete notice = %q, want deleted", m.sessionsSidebar.notice)
	}
}

// TestSidebarDeletesGhostBoundRouteRow pins that a route row bound to a dead
// instance (the instance row is gone but the bound route survives in storage)
// is removable: cleanup must delete bound route rows by name, not only legacy
// rows, so the zombie route cannot stay in storage forever.
func TestSidebarDeletesGhostBoundRouteRow(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "ghost-bound", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	route := findWorktreeRoute(t, sessionInfosFromCatalog(infos), worktree.Name)
	if route.WorktreeInstance.IsZero() {
		t.Fatalf("route row has no instance: %+v", route)
	}
	// Remove the Git worktree and the instance row out-of-band, leaving the
	// instance-bound route row orphaned in storage.
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM worktree_instances WHERE worktree=?`, worktree.Name); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	m := readySidebarModel(t, repoRoot, []chat.SessionInfo{route})
	m.sessionsSidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()
	if len(m.sessions) != 0 {
		t.Fatalf("ghost bound route row still listed: %+v", m.sessions)
	}
	if !strings.Contains(m.sessionsSidebar.notice, "deleted") {
		t.Fatalf("delete notice = %q, want deleted", m.sessionsSidebar.notice)
	}
	checkStore, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer checkStore.Close()
	db, err = sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var routes int
	if err := db.QueryRow(`SELECT count(*) FROM worktree_routes WHERE worktree=?`, worktree.Name).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if routes != 0 {
		t.Fatalf("bound route rows after delete = %d, want 0", routes)
	}
}
