package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

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
