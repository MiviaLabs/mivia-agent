package cliworktree

// context_setup_gaps_test.go covers the remaining uncovered statements in
// context_setup.go and worktree_removal.go: the unwired store seam, the
// non-SQLite session route fallback, the managed registration entry, and the
// removal reactivation path.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestOpenRepositoryContextStoreRequiresWiredSeam(t *testing.T) {
	original := OpenRepositoryContextStoreFunc
	OpenRepositoryContextStoreFunc = nil
	t.Cleanup(func() { OpenRepositoryContextStoreFunc = original })
	if _, err := openRepositoryContextStore(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("openRepositoryContextStore without the seam error = %v", err)
	}
}

func TestRegisterWorktreeRouteForSessionFallsBackToRepositoryStore(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repo, "route-fallback", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// A session without a SQLite context store must fall back to the
	// repository store and register the legacy route there.
	session := newTestSessionForModel("model")
	if err := registerWorktreeRouteForSession(session, repo, worktree); err != nil {
		t.Fatalf("registerWorktreeRouteForSession fallback error = %v", err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalMarkerRoot(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequireLegacyWorktreeRoute(context.Background(), principal, worktree.Name, canonical); err != nil {
		t.Fatalf("legacy route not registered through the fallback: %v", err)
	}
}

func TestRegisterManagedWorktreeOpensRepositoryStore(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repo, "managed-reg", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := registerManagedWorktree(repo, worktree)
	if err != nil {
		t.Fatalf("registerManagedWorktree error = %v", err)
	}
	if instance.Worktree != worktree.Name {
		t.Fatalf("instance worktree = %q, want %q", instance.Worktree, worktree.Name)
	}
	marker, err := ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatalf("ReadWorktreeMarker after registration: %v", err)
	}
	if marker != instance {
		t.Fatalf("marker = %+v, want %+v", marker, instance)
	}
}

func TestRemoveWorktreeLockedReactivatesWhenGitRemovalFails(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "remove-fail", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(worktree.Path, 0o755)
	})
	// Make the physical removal fail: git cannot delete entries from a
	// read-only worktree directory.
	if err := os.Chmod(worktree.Path, 0o500); err != nil {
		t.Fatal(err)
	}
	lock, err := LockWorktreeLifecycle(repo, worktree.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	removed, err := RemoveWorktreeLocked(repo, worktree.Name, "mivia/", lock.File())
	if err == nil {
		t.Fatalf("RemoveWorktreeLocked against a read-only worktree = (%v, nil), want an error", removed)
	}
	// After the failed removal the instance must still be live for the name:
	// either reactivated to active, or held in the deleting state with the
	// failure reported. The hard contract is that the name never drops its
	// instance silently.
	_ = os.Chmod(worktree.Path, 0o755)
	store, serr := openRepositoryContextStore(repo)
	if serr != nil {
		t.Fatal(serr)
	}
	defer store.Close()
	principal, perr := WorktreeRoutePrincipal(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	live, lerr := store.LiveWorktreeInstance(context.Background(), principal, worktree.Name)
	if lerr != nil || live.Instance != instance {
		t.Fatalf("live instance after failed removal = (%+v, %v), want %+v", live, lerr, instance)
	}
}
