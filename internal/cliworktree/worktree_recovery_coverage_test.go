package cliworktree

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestRecoveryCoverageWrapperErrors(t *testing.T) {
	if _, err := AdoptManagedWorktree(t.TempDir(), &vcs.WorktreeInfo{Name: "wt-a", Path: t.TempDir()}); err == nil {
		t.Fatal("adoption outside repository succeeded")
	}
	blocked := blockedContextRoot(t)
	if _, err := RecoverManagedWorktreeRemoval(blocked, "wt-a", "mivia/"); err == nil {
		t.Fatal("recovery opened blocked store")
	}
	if _, err := recoverManagedWorktreeRemovalLocked(blocked, "wt-a", "mivia/", nil); err == nil {
		t.Fatal("locked recovery opened blocked store")
	}
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := recoverManagedWorktreeRemovalInStore(store, repo, strings.Repeat("x", 65), "mivia/"); err == nil {
		t.Fatal("invalid recovery name succeeded")
	}
	lock, err := LockWorktreeLifecycle(repo, "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := recoverManagedWorktreeRemovalInStore(store, repo, "busy", "mivia/"); err == nil {
		t.Fatal("recovery acquired busy lock")
	}
}

func TestRecoveryCoverageRejectsStaleRemovalRows(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	active := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: filepath.Join(repo, "wt-a"), State: contextstate.WorktreeActive}
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, active, "mivia/", nil); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("active recovery error = %v", err)
	}
	deleting := active
	deleting.State = contextstate.WorktreeDeleting
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, deleting, "mivia/", nil); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("missing deleting row error = %v", err)
	}
}

func TestRecoveryCoverageCreationFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "create-missing", ID: "wt_1234567890abcdef"}
	active := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: filepath.Join(repo, "missing"), State: contextstate.WorktreeActive}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, active); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("active creation recovery error = %v", err)
	}
	creating := active
	creating.State = contextstate.WorktreeCreating
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("missing creation row error = %v", err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, creating.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); err == nil {
		t.Fatal("creation recovery without Git worktree succeeded")
	}
}

func TestRecoveryCoverageCreationPathMismatch(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "create-mismatch", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
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
	instance := contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_1234567890abcdef"}
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: filepath.Join(repo, "wrong"), State: contextstate.WorktreeCreating}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, info.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, info); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("mismatched path error = %v", err)
	}
}
