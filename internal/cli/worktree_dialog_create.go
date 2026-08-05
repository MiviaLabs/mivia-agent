package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) createWorktreeAsyncWithPrefix(dir, name, branchPrefix string, dlg *worktreeDialog) tea.Cmd {
	return func() tea.Msg {
		var wt *vcs.WorktreeInfo
		var err error
		if store, ok := m.session.ContextStore().(*storage.SQLite); ok && store != nil {
			wt, err = createManagedWorktreeInStore(store, dir, name, "", branchPrefix)
		} else {
			wt, err = createManagedWorktree(dir, name, "", branchPrefix)
		}
		return worktreeCreatedMsg{wt: wt, err: err, dlg: dlg}
	}
}
