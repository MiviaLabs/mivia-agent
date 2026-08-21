package cliworktree

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

var lifecycleRoutePrincipal = WorktreeRoutePrincipal
var lifecycleCanonicalMarkerRoot = CanonicalMarkerRoot
var lifecycleResolveWorktree = vcs.Resolve

func AdoptManagedWorktree(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	lock, err := LockWorktreeLifecycle(root, wt.Name)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer lock.Close()
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := CanonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	instance, markerErr := ReadWorktreeMarker(wt.Path)
	wroteMarker := false
	if errors.Is(markerErr, os.ErrNotExist) {
		creating, creatingErr := store.CreatingWorktreeInstance(context.Background(), principal, wt.Name)
		if errors.Is(creatingErr, contextstate.ErrWorktreeDeleted) {
			instance, err = newManagedWorktreeInstance(wt.Name)
			if err != nil {
				return contextstate.WorktreeInstance{}, err
			}
			if err := store.BeginWorktreeAdoption(context.Background(), principal, instance, canonicalPath); err != nil {
				return contextstate.WorktreeInstance{}, err
			}
		} else if creatingErr != nil || creating.Instance.Worktree != wt.Name || creating.CanonicalPath != canonicalPath {
			return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
		} else {
			if err := store.RequireLegacyWorktreeRoute(context.Background(), principal, wt.Name, canonicalPath); err != nil {
				return contextstate.WorktreeInstance{}, err
			}
			instance = creating.Instance
		}
		if err := WriteWorktreeMarker(wt.Path, instance); err != nil {
			_ = store.AbandonWorktreeCreation(context.Background(), principal, instance)
			return contextstate.WorktreeInstance{}, err
		}
		wroteMarker = true
	} else if markerErr != nil {
		return contextstate.WorktreeInstance{}, markerErr
	} else {
		creating, err := store.CreatingWorktreeInstance(context.Background(), principal, wt.Name)
		if instance.Worktree != wt.Name || err != nil || creating.Instance != instance || creating.CanonicalPath != canonicalPath {
			return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
		}
	}
	if err := store.RegisterAdoptedWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		if wroteMarker {
			_ = os.Remove(WorktreeMarkerPath(canonicalPath))
			_ = store.AbandonWorktreeCreation(context.Background(), principal, instance)
		}
		return contextstate.WorktreeInstance{}, err
	}
	return instance, nil
}

func RecoverManagedWorktreeRemoval(root, name, branchPrefix string) (bool, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return false, err
	}
	defer store.Close()
	return recoverManagedWorktreeRemovalInStore(store, root, name, branchPrefix)
}

func recoverManagedWorktreeRemovalLocked(root, name, branchPrefix string, lease *os.File) (bool, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return false, err
	}
	defer store.Close()
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return false, err
	}
	return recoverManagedWorktreeRemovalInStoreLocked(store, root, sanitized, branchPrefix, lease)
}

func recoverManagedWorktreeRemovalInStore(store *storage.SQLite, root, name, branchPrefix string) (bool, error) {
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return false, err
	}
	lock, err := LockWorktreeLifecycle(root, sanitized)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	return recoverManagedWorktreeRemovalInStoreLocked(store, root, sanitized, branchPrefix, lock.File())
}

func recoverManagedWorktreeRemovalInStoreLocked(store *storage.SQLite, root, sanitized, branchPrefix string, lease *os.File) (bool, error) {
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return false, err
	}
	deleting, err := store.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		return false, err
	}
	for _, info := range deleting {
		if info.Instance.Worktree != sanitized {
			continue
		}
		return true, RecoverManagedWorktreeRemovalInfoInStoreLocked(store, root, info, branchPrefix, lease)
	}
	return false, nil
}

// RecoverManagedWorktreeRemovalInfoInStoreLocked implements recover managed worktree removal info in store locked.
func RecoverManagedWorktreeRemovalInfoInStoreLocked(store *storage.SQLite, root string, info contextstate.WorktreeInstanceInfo, branchPrefix string, lease *os.File) error {
	if info.State != contextstate.WorktreeDeleting {
		return contextstate.ErrWorktreeDeleted
	}
	principal, err := lifecycleRoutePrincipal(root)
	if err != nil {
		return err
	}
	deleting, err := store.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		return err
	}
	found := false
	for _, current := range deleting {
		if current == info {
			found = true
			break
		}
	}
	if !found {
		return contextstate.ErrWorktreeDeleted
	}
	worktree, err := lifecycleResolveWorktree(context.Background(), root, info.Instance.Worktree)
	if err != nil {
		return err
	}
	if worktree != nil {
		instance, markerErr := ReadWorktreeMarker(worktree.Path)
		path, pathErr := lifecycleCanonicalMarkerRoot(worktree.Path)
		if pathErr != nil {
			return pathErr
		}
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			return markerErr
		}
		if markerErr != nil || instance != info.Instance || path != info.CanonicalPath {
			_, err = store.DeleteWorktreeSessions(context.Background(), principal, info.Instance)
			return err
		}
		if err := vcs.RemoveWithPrefixLease(context.Background(), root, info.Instance.Worktree, branchPrefix, lease); err != nil {
			return err
		}
	}
	_, err = store.DeleteWorktreeSessions(context.Background(), principal, info.Instance)
	return err
}

// RecoverManagedWorktreeCreationInStore implements recover managed worktree creation in store.
func RecoverManagedWorktreeCreationInStore(store *storage.SQLite, root string, info contextstate.WorktreeInstanceInfo) (*vcs.WorktreeInfo, error) {
	lock, err := LockWorktreeLifecycle(root, info.Instance.Worktree)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return recoverManagedWorktreeCreationInStoreLocked(store, root, info)
}

func recoverManagedWorktreeCreationInStoreLocked(store *storage.SQLite, root string, info contextstate.WorktreeInstanceInfo) (*vcs.WorktreeInfo, error) {
	if info.State != contextstate.WorktreeCreating {
		return nil, contextstate.ErrWorktreeDeleted
	}
	principal, err := lifecycleRoutePrincipal(root)
	if err != nil {
		return nil, err
	}
	current, err := store.CreatingWorktreeInstance(context.Background(), principal, info.Instance.Worktree)
	if err != nil || current != info {
		return nil, contextstate.ErrWorktreeDeleted
	}
	worktree, err := lifecycleResolveWorktree(context.Background(), root, info.Instance.Worktree)
	if err != nil {
		return nil, err
	}
	if worktree == nil {
		return nil, fmt.Errorf("worktree creation recovery requires Git worktree %q", info.CanonicalPath)
	}
	path, err := CanonicalMarkerRoot(worktree.Path)
	if err != nil || path != info.CanonicalPath {
		return nil, contextstate.ErrWorktreeDeleted
	}
	if err := completeManagedWorktreeCreationInStore(store, root, worktree, info.Instance); err != nil {
		return nil, err
	}
	return worktree, nil
}
