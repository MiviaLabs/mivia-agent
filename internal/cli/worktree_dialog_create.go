package cli

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) createWorktreeAsyncWithPrefix(dir, name, branchPrefix string, dlg *worktreeDialog) tea.Cmd {
	return func() tea.Msg {
		wt, err := vcs.CreateWithPrefix(context.Background(), dir, name, "", branchPrefix)
		if err == nil {
			_, err = registerManagedWorktreeForSession(m.session, dir, wt)
		}
		return worktreeCreatedMsg{wt: wt, err: err, dlg: dlg}
	}
}
