//go:build unix

package cliworktree

import (
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestLifecycleExactAdoptRejectsNilWorktree(t *testing.T) {
	if _, err := AdoptManagedWorktree(t.TempDir(), nil); err == nil {
		t.Fatal("nil worktree adoption succeeded")
	}
}

func TestLifecycleExactRecoverySkipsOtherDeletingName(t *testing.T) {
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
	instance := contextstate.WorktreeInstance{Worktree: "other", ID: "wt_5555555555555555"}
	path := filepath.Join(repo, ".mivia", "worktrees", "other")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverManagedWorktreeRemovalInStoreLocked(store, repo, "target", "mivia/", nil)
	if err != nil || recovered {
		t.Fatalf("unrelated recovery = %t, %v; want false, nil", recovered, err)
	}
}

func TestLifecycleExactClosedStoreAndResolveErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	info := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}, State: contextstate.WorktreeDeleting}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); err == nil {
		t.Fatal("closed deletion store succeeded")
	}

	store, err = openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".mivia", "worktrees", "wt-a")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, info.Instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, info.Instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, info.Instance); err != nil {
		t.Fatal(err)
	}
	info.CanonicalPath = path
	t.Setenv("PATH", t.TempDir())
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); err == nil {
		t.Fatal("Git resolution failure was hidden")
	}
}

func TestLifecycleExactRemovalAndCreationFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := CreateManagedWorktree(repo, "remove-error", "HEAD", "mivia/")
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
	instance, err := ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: worktree.Path, State: contextstate.WorktreeDeleting}
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "bad-prefix", nil); err == nil {
		t.Fatal("invalid removal prefix succeeded")
	}

	created, err := vcs.CreateWithPrefix(context.Background(), repo, "create-error", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	createInstance := contextstate.WorktreeInstance{Worktree: created.Name, ID: "wt_2222222222222222"}
	createInfo := contextstate.WorktreeInstanceInfo{Instance: createInstance, CanonicalPath: created.Path, State: contextstate.WorktreeCreating}
	if err := store.BeginWorktreeCreation(context.Background(), principal, createInstance, created.Path); err != nil {
		t.Fatal(err)
	}
	markerDir := filepath.Join(created.Path, ".mivia")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, worktreeMarkerName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, createInfo); err == nil || errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("malformed creation marker error = %v", err)
	}
}

func TestLifecycleFaultSeamsRecoveryPrincipalErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sentinel := errors.New("principal fault")
	original := lifecycleRoutePrincipal
	lifecycleRoutePrincipal = func(string) (contextstate.Principal, error) {
		return contextstate.Principal{}, sentinel
	}
	t.Cleanup(func() { lifecycleRoutePrincipal = original })
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	deleting := contextstate.WorktreeInstanceInfo{Instance: instance, State: contextstate.WorktreeDeleting}
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, deleting, "mivia/", nil); !errors.Is(err, sentinel) {
		t.Fatalf("removal principal error = %v", err)
	}
	creating := contextstate.WorktreeInstanceInfo{Instance: instance, State: contextstate.WorktreeCreating}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); !errors.Is(err, sentinel) {
		t.Fatalf("creation principal error = %v", err)
	}
}

func TestLifecycleFaultSeamsRecoveryPathAndResolveErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, info := prepareDeletingLifecycleInfo(t, repo)
	defer store.Close()
	sentinel := errors.New("recovery fault")
	originalResolve, originalCanonical := lifecycleResolveWorktree, lifecycleCanonicalMarkerRoot
	t.Cleanup(func() {
		lifecycleResolveWorktree, lifecycleCanonicalMarkerRoot = originalResolve, originalCanonical
	})
	lifecycleResolveWorktree = func(context.Context, string, string) (*vcs.WorktreeInfo, error) {
		return &vcs.WorktreeInfo{Name: info.Instance.Worktree, Path: info.CanonicalPath}, nil
	}
	lifecycleCanonicalMarkerRoot = func(string) (string, error) { return "", sentinel }
	if err := RecoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); !errors.Is(err, sentinel) {
		t.Fatalf("canonical path error = %v", err)
	}

	creating := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "new", ID: "wt_2222222222222222"}, State: contextstate.WorktreeCreating}
	creating.CanonicalPath = filepath.Join(repo, ".mivia", "worktrees", "new")
	principal, err := WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, creating.Instance, creating.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	lifecycleResolveWorktree = func(context.Context, string, string) (*vcs.WorktreeInfo, error) { return nil, sentinel }
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); !errors.Is(err, sentinel) {
		t.Fatalf("resolve error = %v", err)
	}
}

func prepareDeletingLifecycleInfo(t *testing.T, repo string) (*storage.SQLite, contextstate.WorktreeInstanceInfo) {
	t.Helper()
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := WorktreeRoutePrincipal(repo)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "delete", ID: "wt_3333333333333333"}
	path := filepath.Join(repo, ".mivia", "worktrees", instance.Worktree)
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: path, State: contextstate.WorktreeDeleting}
}
func TestLifecycleExactLockErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	if _, err := recoverManagedWorktreeRemovalLocked(repo, strings.Repeat("x", 65), "mivia/", nil); err == nil {
		t.Fatal("invalid locked recovery name succeeded")
	}
	lock, err := LockWorktreeLifecycle(repo, "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "busy", ID: "wt_1111111111111111"}, State: contextstate.WorktreeDeleting}
	if err := recoverManagedWorktreeRemovalInfoInStore(store, repo, info, "mivia/"); err == nil {
		t.Fatal("busy recovery lock succeeded")
	}
}
func TestLifecycleFaultSeamCreationRecoveryLockError(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sentinel := errors.New("creation recovery lock fault")
	restoreLifecycleRoot := vcs.SetLifecycleGitRootOpenerForTest(
		func(string) (*os.Root, error) { return nil, sentinel })
	t.Cleanup(restoreLifecycleRoot)
	info := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}, State: contextstate.WorktreeCreating}
	if _, err := RecoverManagedWorktreeCreationInStore(store, repo, info); !errors.Is(err, sentinel) {
		t.Fatalf("creation recovery lock error = %v", err)
	}
}
