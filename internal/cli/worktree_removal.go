package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// ErrUnmanagedWorktree marks a worktree that has no valid lifecycle binding:
// its marker is unreadable or its instance is not active in storage. The
// caller removes the physical worktree directly and cleans storage rows by
// name, so HDD space is freed even when the storage layer has no entry.
var ErrUnmanagedWorktree = errors.New("worktree has no managed lifecycle binding")

// RemoveUnmanagedWorktree removes a worktree that has no valid lifecycle
// binding. It removes the physical worktree first so HDD space is always
// freed, then prunes stale Git metadata and cleans storage rows for the name.
func RemoveUnmanagedWorktree(root string, wt *vcs.WorktreeInfo, branchPrefix string, lease *os.File) error {
	if wt == nil {
		return fmt.Errorf("worktree route requires a worktree")
	}
	store, err := OpenRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	if err := vcs.RemoveWithPrefixLease(context.Background(), root, wt.Name, branchPrefix, lease); err != nil {
		var notFound vcs.WorktreeNotFoundError
		if !errors.As(err, &notFound) {
			return err
		}
		// The directory is already gone; pruning below clears the stale entry.
	}
	if err := vcs.Prune(context.Background(), root, wt.Name); err != nil {
		return err
	}
	_, err = CleanupStaleWorktreeRows(store, principal, wt.Name)
	return err
}

// cleanupStaleWorktreeStorage opens the repository store and cleans every
// storage row for one worktree name. It reports whether any row existed.
func cleanupStaleWorktreeStorage(root, name string) (bool, error) {
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return false, err
	}
	store, err := OpenRepositoryContextStore(root)
	if err != nil {
		return false, err
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return false, err
	}
	return CleanupStaleWorktreeRows(store, principal, sanitized)
}

// CleanupStaleWorktreeRows fences and removes every storage row for one
// worktree name, tombstoning its sessions. It reports whether any row
// existed. Call it only when the physical worktree is gone: the name owns at
// most one non-deleted instance, so a same-name replacement is never touched.
func CleanupStaleWorktreeRows(store *storage.SQLite, principal contextstate.Principal, name string) (bool, error) {
	cleaned := false
	live, err := store.LiveWorktreeInstance(context.Background(), principal, name)
	switch {
	case err == nil:
		cleaned = true
		switch live.State {
		case contextstate.WorktreeActive:
			if err := store.BeginWorktreeDeletion(context.Background(), principal, live.Instance); err != nil {
				return cleaned, err
			}
			if _, err := store.DeleteWorktreeSessions(context.Background(), principal, live.Instance); err != nil {
				return cleaned, err
			}
		case contextstate.WorktreeCreating:
			if err := store.AbandonWorktreeCreation(context.Background(), principal, live.Instance); err != nil {
				return cleaned, err
			}
		case contextstate.WorktreeDeleting:
			if _, err := store.DeleteWorktreeSessions(context.Background(), principal, live.Instance); err != nil {
				return cleaned, err
			}
		}
		// The legacy launch route (instance_id IS NULL) belongs to the name,
		// not the instance, so DeleteWorktreeSessions leaves it behind.
		removed, err := store.DeleteWorktreeRoute(context.Background(), principal, name)
		if err != nil {
			return cleaned, err
		}
		if removed > 0 {
			cleaned = true
		}
	case errors.Is(err, contextstate.ErrWorktreeDeleted):
		// No live instance row for the name. Remove every route row, bound or
		// legacy: a bound route of a dead instance would otherwise stay in
		// storage forever and resurface as a zombie row.
		removed, err := store.DeleteWorktreeRoutesByName(context.Background(), principal, name)
		if err != nil {
			return cleaned, err
		}
		if removed > 0 {
			cleaned = true
		}
	default:
		return cleaned, err
	}
	return cleaned, nil
}

// FinishManagedWorktreeRemoval implements finish managed worktree removal.
func FinishManagedWorktreeRemoval(root string, instance contextstate.WorktreeInstance) error {
	return FinishManagedWorktreeRemovalInStore(nil, root, instance)
}

// FinishManagedWorktreeRemovalInStore implements finish managed worktree removal in store.
func FinishManagedWorktreeRemovalInStore(store *storage.SQLite, root string, instance contextstate.WorktreeInstance) error {
	ownedStore := false
	if store == nil {
		var err error
		store, err = OpenRepositoryContextStore(root)
		if err != nil {
			return err
		}
		ownedStore = true
	}
	if ownedStore {
		defer store.Close()
	}
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	if _, err := store.DeleteWorktreeSessions(context.Background(), principal, instance); err != nil {
		return err
	}
	// The legacy launch route (instance_id IS NULL) belongs to the name, not
	// the instance, so DeleteWorktreeSessions leaves it behind. Remove it too,
	// or the removed worktree stays visible in the session list forever.
	_, err = store.DeleteWorktreeRoute(context.Background(), principal, instance.Worktree)
	return err
}

// RemoveWorktreeLocked removes one worktree by name under an acquired
// lifecycle lock. It runs deletion recovery, the managed removal, the
// unmanaged fallback for worktrees without a storage binding, and ghost
// cleanup for names whose Git worktree is gone. It reports whether the
// worktree or its storage rows were removed.
func RemoveWorktreeLocked(root, name, branchPrefix string, lease *os.File) (bool, error) {
	if recovered, err := recoverManagedWorktreeRemovalLocked(root, name, branchPrefix, lease); err != nil {
		return false, err
	} else if recovered {
		return true, nil
	}
	worktree, err := vcs.Resolve(context.Background(), root, name)
	if err != nil {
		return false, err
	}
	if worktree == nil {
		// The Git worktree is gone. Clean storage rows for the name so a
		// stale launch route disappears from the session list.
		cleaned, err := cleanupStaleWorktreeStorage(root, name)
		if err != nil {
			return false, err
		}
		return cleaned, nil
	}
	if WorktreeContainsCurrentDir(worktree.Path) {
		return false, fmt.Errorf("cannot remove the current worktree")
	}
	instance, err := beginManagedWorktreeRemoval(root, worktree)
	if errors.Is(err, ErrUnmanagedWorktree) {
		// The worktree has no valid lifecycle binding (missing marker or no
		// storage entry). Remove it directly so its HDD space is freed.
		if err := RemoveUnmanagedWorktree(root, worktree, branchPrefix, lease); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err := vcs.RemoveWithPrefixLease(context.Background(), root, worktree.Name, branchPrefix, lease); err != nil {
		if reactivateErr := ReactivateManagedWorktree(root, instance); reactivateErr != nil {
			return false, fmt.Errorf("%w; session lifecycle recovery failed: %v", err, reactivateErr)
		}
		return false, err
	}
	if err := FinishManagedWorktreeRemoval(root, instance); err != nil {
		return false, fmt.Errorf("removed %q but could not clean its sessions: %w", worktree.Name, err)
	}
	return true, nil
}
