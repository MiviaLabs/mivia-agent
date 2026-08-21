package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func createManagedWorktreeWithInstance(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, contextstate.WorktreeInstance, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return nil, contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	var instance contextstate.WorktreeInstance
	worktree, err := createManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, &instance)
	return worktree, instance, err
}

func createManagedWorktreeInStoreWithInstance(store *storage.SQLite, root, name, baseRef, branchPrefix string, result *contextstate.WorktreeInstance) (*vcs.WorktreeInfo, error) {
	lock, err := lockWorktreeLifecycle(root, name)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return createManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lock.File())
}

func (m *tuiModel) createWorktreeAsyncWithPrefix(dir, name, branchPrefix string, dlg *worktreeDialog) tea.Cmd {
	return func() tea.Msg {
		var wt *vcs.WorktreeInfo
		var instance contextstate.WorktreeInstance
		var err error
		if store, ok := m.session.ContextStore().(*storage.SQLite); ok && store != nil {
			wt, err = createManagedWorktreeInStoreWithInstance(store, dir, name, "", branchPrefix, &instance)
		} else {
			wt, instance, err = createManagedWorktreeWithInstance(dir, name, "", branchPrefix)
		}
		return worktreeCreatedMsg{wt: wt, instance: instance, err: err, dlg: dlg}
	}
}
