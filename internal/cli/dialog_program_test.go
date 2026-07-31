package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDialogProgramResizeIntegration(t *testing.T) {
	m := newReadyChatModel(24, 90)
	for _, command := range []string{"/help", "/status", "/tools", "/sessions"} {
		m.handleSlash(command)
		if command == "/tools" && !m.modalOpen() {
			// The harness session has no registry; exercise the same producer
			// path with a representative tool snapshot.
			m.setOverlay(m.newToolsDialog([]string{"read_file"}))
		}
		if !m.modalOpen() {
			t.Fatalf("%s did not open a modal", command)
		}
		view := m.View()
		if view == "" || !strings.Contains(view, "┌") && !strings.Contains(view, "│") {
			t.Fatalf("%s did not render a panel over the base: %q", command, view)
		}
		_, _ = m.Update(tea.WindowSizeMsg{Width: 50, Height: 14})
		_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		m.closeModal()
		m.View()
	}
	m.textarea.SetValue("")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("after modal")})
	if got := m.textarea.Value(); got != "after modal" {
		t.Fatalf("post-modal chat input did not reach composer: %q", got)
	}
}
