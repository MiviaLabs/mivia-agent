package cli

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func (m *tuiModel) openWorktreeDialog() {
	m.closeSuggest()
	wtDir := m.resolveRepoRoot()
	list, err := vcs.List(context.Background(), wtDir)
	if err != nil {
		m.worktreeDlg = newWorktreeDialog(nil)
		m.worktreeDlg.setNotice(err.Error(), true)
		m.hitMap.invalidate()
		return
	}
	store, storeErr := openRepositoryContextStore(wtDir)
	if storeErr == nil {
		principal, principalErr := worktreeRoutePrincipal(wtDir)
		if principalErr == nil {
			deleting, deleteErr := store.ListDeletingWorktreeInstances(context.Background(), principal)
			if deleteErr == nil {
				for _, info := range deleting {
					recovery := vcs.WorktreeInfo{Name: info.Instance.Worktree, Branch: "recovery required", Path: info.CanonicalPath}
					found := false
					for index, worktree := range list {
						if worktree.Name == info.Instance.Worktree {
							list[index] = recovery
							found = true
							break
						}
					}
					if !found {
						list = append(list, recovery)
					}
				}
			}
		}
		_ = store.Close()
	}
	m.worktreeDlg = newWorktreeDialog(list)
	m.hitMap.invalidate()
}
