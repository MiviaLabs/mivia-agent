package cli

import tea "github.com/charmbracelet/bubbletea"

// Update dispatches Bubble Tea messages through the TUI message-kind handler.
func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.handleTUIMessage(msg)
}

// handleTUIMessage is kept as a private seam so message-kind handlers can be
// split without changing Bubble Tea's model and command return contract.
func (m *tuiModel) handleTUIMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.updateMessage(msg)
}
