package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// errUnmanagedWorktree marks a worktree that has no valid lifecycle binding:
// its marker is unreadable or its instance is not active in storage. The
// caller removes the physical worktree directly and cleans storage rows by
// name, so HDD space is freed even when the storage layer has no entry.
var errUnmanagedWorktree = errors.New("worktree has no managed lifecycle binding")

// removeUnmanagedWorktree removes a worktree that has no valid lifecycle
// binding. It removes the physical worktree first so HDD space is always
// freed, then prunes stale Git metadata and cleans storage rows for the name.
func removeUnmanagedWorktree(root string, wt *vcs.WorktreeInfo, branchPrefix string, lease *os.File) error {
	if wt == nil {
		return fmt.Errorf("worktree route requires a worktree")
	}
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
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
	if err := vcs.Prune(context.Background(), root); err != nil {
		return err
	}
	_, err = cleanupStaleWorktreeRows(store, principal, wt.Name)
	return err
}

// cleanupStaleWorktreeStorage opens the repository store and cleans every
// storage row for one worktree name. It reports whether any row existed.
func cleanupStaleWorktreeStorage(root, name string) (bool, error) {
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return false, err
	}
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return false, err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return false, err
	}
	return cleanupStaleWorktreeRows(store, principal, sanitized)
}

// cleanupStaleWorktreeRows fences and removes every storage row for one
// worktree name, tombstoning its sessions. It reports whether any row
// existed. Call it only when the physical worktree is gone: the name owns at
// most one non-deleted instance, so a same-name replacement is never touched.
func cleanupStaleWorktreeRows(store *storage.SQLite, principal contextstate.Principal, name string) (bool, error) {
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
	case errors.Is(err, contextstate.ErrWorktreeDeleted):
		// No live instance row for the name.
	default:
		return cleaned, err
	}
	removed, err := store.DeleteWorktreeRoute(context.Background(), principal, name)
	if err != nil {
		return cleaned, err
	}
	if removed > 0 {
		cleaned = true
	}
	return cleaned, nil
}

func finishManagedWorktreeRemoval(root string, instance contextstate.WorktreeInstance) error {
	return finishManagedWorktreeRemovalInStore(nil, root, instance)
}

func finishManagedWorktreeRemovalForSession(sess *chat.Session, root string, instance contextstate.WorktreeInstance) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return finishManagedWorktreeRemovalInStore(store, root, instance)
	}
	return finishManagedWorktreeRemoval(root, instance)
}

func finishManagedWorktreeRemovalInStore(store *storage.SQLite, root string, instance contextstate.WorktreeInstance) error {
	ownedStore := false
	if store == nil {
		var err error
		store, err = openRepositoryContextStore(root)
		if err != nil {
			return err
		}
		ownedStore = true
	}
	if ownedStore {
		defer store.Close()
	}
	principal, err := worktreeRoutePrincipal(root)
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
