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

func snapshotWorktreeDialogBindings(store *storage.SQLite, principal contextstate.Principal, worktrees []vcs.WorktreeInfo, recoveries []contextstate.WorktreeInstanceInfo) map[string]worktreeDialogBinding {
	recoveryNames := make(map[string]bool, len(recoveries))
	for _, info := range recoveries {
		recoveryNames[info.Instance.Worktree] = true
	}
	bindings := make(map[string]worktreeDialogBinding, len(worktrees))
	for _, worktree := range worktrees {
		if recoveryNames[worktree.Name] {
			continue
		}
		bindings[worktree.Name] = snapshotWorktreeDialogBinding(store, principal, worktree)
	}
	return bindings
}

func snapshotWorktreeDialogBinding(store *storage.SQLite, principal contextstate.Principal, worktree vcs.WorktreeInfo) worktreeDialogBinding {
	canonicalPath, err := canonicalMarkerRoot(worktree.Path)
	if err != nil {
		return worktreeDialogBinding{Err: err}
	}
	instance, markerErr := readWorktreeMarker(worktree.Path)
	if markerErr == nil {
		if instance.Worktree != worktree.Name {
			return worktreeDialogBinding{Err: contextstate.ErrWorktreeDeleted}
		}
		if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
			return worktreeDialogBinding{Err: err}
		}
		return worktreeDialogBinding{Instance: instance}
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return worktreeDialogBinding{Err: markerErr}
	}
	info, legacy, err := classifyMissingWorktreeMarker(store, principal, worktree.Name, canonicalPath)
	if err != nil {
		return worktreeDialogBinding{Err: err}
	}
	if !info.Instance.IsZero() {
		return worktreeDialogBinding{Err: contextstate.ErrWorktreeDeleted}
	}
	if legacy {
		return worktreeDialogBinding{Err: fmt.Errorf("worktree %q requires adoption; run mivia worktree adopt %s", worktree.Name, worktree.Name)}
	}
	return worktreeDialogBinding{}
}

func addWorktreeRecoveryRows(list []vcs.WorktreeInfo, infos []contextstate.WorktreeInstanceInfo) []vcs.WorktreeInfo {
	for _, info := range infos {
		found := false
		for _, worktree := range list {
			if worktree.Name == info.Instance.Worktree {
				found = true
				break
			}
		}
		if !found {
			list = append(list, vcs.WorktreeInfo{Name: info.Instance.Worktree, Path: info.CanonicalPath})
		}
	}
	return list
}
