package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func createManagedWorktreeWithInstance(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, contextstate.WorktreeInstance, error) {
	store, err := cli.OpenRepositoryContextStore(root)
	if err != nil {
		return nil, contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	var instance contextstate.WorktreeInstance
	worktree, err := CreateManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, &instance)
	return worktree, instance, err
}

// CreateManagedWorktreeInStoreWithInstance is relocated to internal/cli (pure
// business logic with no TUI dependency); aliased here so this package's own
// call sites are unchanged.
var CreateManagedWorktreeInStoreWithInstance = cliworktree.CreateManagedWorktreeInStoreWithInstance

func (m *TUIModel) createWorktreeAsyncWithPrefix(dir, name, branchPrefix string, dlg *worktreeDialog) tea.Cmd {
	return func() tea.Msg {
		var wt *vcs.WorktreeInfo
		var instance contextstate.WorktreeInstance
		var err error
		if store, ok := m.session.ContextStore().(*storage.SQLite); ok && store != nil {
			wt, err = CreateManagedWorktreeInStoreWithInstance(store, dir, name, "", branchPrefix, &instance)
		} else {
			wt, instance, err = createManagedWorktreeWithInstance(dir, name, "", branchPrefix)
		}
		return worktreeCreatedMsg{wt: wt, instance: instance, err: err, dlg: dlg}
	}
}
