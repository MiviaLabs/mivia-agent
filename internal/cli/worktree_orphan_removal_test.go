package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	_ "modernc.org/sqlite"
)

// deleteWorktreeStorageRows removes every storage row for one worktree name.
// It simulates a worktree whose storage binding was lost (for example a
// database reset) while its Git worktree stayed on disk.
func deleteWorktreeStorageRows(t *testing.T, storePath, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM worktree_instances WHERE worktree=?`, name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM worktree_routes WHERE worktree=?`, name); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeCommandRemoveOrphanWithoutMarker pins the fix where a worktree
// with no marker file and no storage rows could not be removed, so its
// directory stayed on disk and consumed HDD space forever.
func TestWorktreeCommandRemoveOrphanWithoutMarker(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "orphan-nomarker", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(worktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	deleteWorktreeStorageRows(t, filepath.Join(repoRoot, "repository.db"), worktree.Name)

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove orphan without marker: %v", err)
	}
	if output.String() != "removed worktree \"orphan-nomarker\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("resolve removed orphan = %v, %v", resolved, err)
	}
	if _, err := os.Stat(worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still exists: %v", err)
	}
}

// TestWorktreeCommandRemoveOrphanWithMarkerNoStorage pins the fix where a
// worktree whose marker survives but whose storage rows are gone could not be
// removed: ValidateActiveWorktreeInstance refused it with ErrWorktreeDeleted.
func TestWorktreeCommandRemoveOrphanWithMarkerNoStorage(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "orphan-marker", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	deleteWorktreeStorageRows(t, filepath.Join(repoRoot, "repository.db"), worktree.Name)
	if _, err := readWorktreeMarker(worktree.Path); err != nil {
		t.Fatalf("marker must survive: %v", err)
	}

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove orphan with marker: %v", err)
	}
	if output.String() != "removed worktree \"orphan-marker\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("resolve removed orphan = %v, %v", resolved, err)
	}
	if _, err := os.Stat(worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still exists: %v", err)
	}
}

// TestWorktreeCommandRemoveGhostCleansStorage pins the fix where a worktree
// whose Git entry is gone but whose storage rows remain stayed visible in the
// session list forever: remove returned "worktree not found" and never
// cleaned the zombie rows.
func TestWorktreeCommandRemoveGhostCleansStorage(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "ghost-route", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	// Remove the Git worktree out-of-band, leaving the storage rows behind.
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("git worktree still resolves = %v, %v", resolved, err)
	}

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove ghost: %v", err)
	}
	if output.String() != "removed worktree \"ghost-route\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LiveWorktreeInstance(context.Background(), principal, worktree.Name); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("live instance after ghost cleanup = %v, want ErrWorktreeDeleted", err)
	}
}

// TestWorktreeCommandRemoveMalformedMarkerFallsBack pins the fix where a
// corrupted marker file blocked removal of an otherwise live worktree.
func TestWorktreeCommandRemoveMalformedMarkerFallsBack(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "malformed-live", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreeMarkerPath(worktree.Path), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove malformed-marker worktree: %v", err)
	}
	if output.String() != "removed worktree \"malformed-live\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("resolve removed worktree = %v, %v", resolved, err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LiveWorktreeInstance(context.Background(), principal, worktree.Name); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("live instance after removal = %v, want ErrWorktreeDeleted", err)
	}
}

// TestWorktreeCommandRemoveUnknownNameStillFails pins that removing a name
// with no Git worktree and no storage rows keeps the "not found" error: the
// ghost-cleanup path must report success only when it cleaned a real row.
func TestWorktreeCommandRemoveUnknownNameStillFails(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	err := runWorktreeWithIO([]string{"remove", "never-existed", "--workspace", repoRoot}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("remove unknown name error = %v, want not found", err)
	}
}

// TestWorktreeCommandRemoveGhostWithLegacyRouteReportsRemoved pins that a
// ghost whose only remaining storage row is a legacy route reports success:
// the route is removed from the session list, so "not found" would lie.
func TestWorktreeCommandRemoveGhostWithLegacyRouteReportsRemoved(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "legacy-ghost", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repoRoot)
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove legacy ghost: %v", err)
	}
	if output.String() != "removed worktree \"legacy-ghost\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
}

// TestWorktreeCommandRemoveAdoptedWorktreeCleansLegacyRoute pins that a
// normal removal also removes the legacy launch route, so no zombie worktree
// row stays visible in the session list after the worktree is gone.
func TestWorktreeCommandRemoveAdoptedWorktreeCleansLegacyRoute(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := vcs.Create(context.Background(), repoRoot, "adopted-remove", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := adoptManagedWorktree(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove adopted worktree: %v", err)
	}
	if output.String() != "removed worktree \"adopted-remove\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	db, err := sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var legacyRoutes int
	if err := db.QueryRow(`SELECT count(*) FROM worktree_routes WHERE worktree=? AND instance_id IS NULL`, worktree.Name).Scan(&legacyRoutes); err != nil {
		t.Fatal(err)
	}
	if legacyRoutes != 0 {
		t.Fatalf("legacy routes after removal = %d, want 0", legacyRoutes)
	}
}

// TestWorktreeDialogDeletesOrphanWorktree pins the fix where the worktree
// dialog could not delete a worktree with no storage binding: the delete
// confirm failed in beginManagedWorktreeRemoval and the directory stayed on
// disk. The dialog must fall back to unmanaged removal.
func TestWorktreeDialogDeletesOrphanWorktree(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "dialog-orphan", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(worktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	deleteWorktreeStorageRows(t, filepath.Join(repoRoot, "repository.db"), worktree.Name)

	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("dialog rows = %#v", m.worktreeDlg)
	}
	m.handleChatKey("d", false)
	m.handleChatKey("y", false)
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("dialog rows after delete = %#v", m.worktreeDlg)
	}
	if m.worktreeDlg.notice != `deleted "dialog-orphan"` {
		t.Fatalf("delete notice = %q", m.worktreeDlg.notice)
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved != nil {
		t.Fatalf("resolve removed orphan = %v, %v", resolved, err)
	}
	if _, err := os.Stat(worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still exists: %v", err)
	}
}
