package legacytui

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func (m *TUIModel) openWorktreeDialog() {
	m.closeSuggest()
	m.closeHistory()
	wtDir := m.resolveRepoRoot()
	list, err := vcs.List(context.Background(), wtDir)
	if err != nil {
		m.worktreeDlg = newWorktreeDialog(nil)
		m.worktreeDlg.setNotice(err.Error(), true)
		m.hitMap.invalidate()
		return
	}
	var recoveries []contextstate.WorktreeInstanceInfo
	bindings := make(map[string]cliworktree.WorktreeDialogBinding)
	store, closeStore, storeErr := m.worktreeLifecycleStore(wtDir)
	lifecycleErr := storeErr
	if storeErr == nil {
		principal, _ := cliworktree.WorktreeRoutePrincipal(wtDir)
		{
			creating, createErr := store.ListCreatingWorktreeInstances(context.Background(), principal)
			if createErr != nil {
				lifecycleErr = createErr
			} else {
				list = cliworktree.AddWorktreeRecoveryRows(list, creating)
				recoveries = append(recoveries, creating...)
			}
			deleting, deleteErr := store.ListDeletingWorktreeInstances(context.Background(), principal)
			if deleteErr != nil {
				lifecycleErr = deleteErr
			} else {
				list = cliworktree.AddWorktreeRecoveryRows(list, deleting)
				recoveries = append(recoveries, deleting...)
			}
			if lifecycleErr == nil {
				bindings = cliworktree.SnapshotWorktreeDialogBindings(store, principal, list, recoveries)
			}
		}
		closeStore()
	}
	m.worktreeDlg = newWorktreeDialog(list)
	m.worktreeDlg.bindings = bindings
	for _, info := range recoveries {
		m.worktreeDlg.setRecovery(info)
	}
	if lifecycleErr != nil {
		m.worktreeDlg.lifecycleUnavailable = true
		m.worktreeDlg.setNotice(lifecycleErr.Error(), true)
	}
	m.hitMap.invalidate()
}

func (m *TUIModel) worktreeLifecycleStore(root string) (*storage.SQLite, func(), error) {
	if m.session != nil && m.session.ContextStore() != nil {
		store, ok := m.session.ContextStore().(*storage.SQLite)
		if !ok {
			return nil, func() {}, fmt.Errorf("worktree lifecycle requires a SQLite session store")
		}
		return store, func() {}, nil
	}
	store, err := cli.OpenRepositoryContextStore(root)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() { _ = store.Close() }, nil
}
