package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func adoptManagedWorktree(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	instance, markerErr := readWorktreeMarker(wt.Path)
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
		if err := writeWorktreeMarker(wt.Path, instance); err != nil {
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
			_ = os.Remove(worktreeMarkerPath(canonicalPath))
			_ = store.AbandonWorktreeCreation(context.Background(), principal, instance)
		}
		return contextstate.WorktreeInstance{}, err
	}
	return instance, nil
}

func recoverManagedWorktreeRemoval(root, name, branchPrefix string) (bool, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return false, err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return false, err
	}
	deleting, err := store.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		return false, err
	}
	for _, info := range deleting {
		if info.Instance.Worktree != name {
			continue
		}
		worktree, err := vcs.Resolve(context.Background(), root, name)
		if err != nil {
			return true, err
		}
		if worktree != nil {
			instance, err := readWorktreeMarker(worktree.Path)
			path, err := canonicalMarkerRoot(worktree.Path)
			if err != nil {
				return true, err
			}
			if err != nil || instance != info.Instance || path != info.CanonicalPath {
				_, err = store.DeleteWorktreeSessions(context.Background(), principal, info.Instance)
				return true, err
			}
			if err := vcs.RemoveWithPrefix(context.Background(), root, name, branchPrefix); err != nil {
				return true, err
			}
		}
		_, err = store.DeleteWorktreeSessions(context.Background(), principal, info.Instance)
		return true, err
	}
	return false, nil
}
