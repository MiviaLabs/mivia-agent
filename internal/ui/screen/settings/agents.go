package settings

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// agentsSection is the Agents settings section: browse subagent role
// definitions and remove one. Editing a definition's fields (tools,
// skills, model binding) needs the same kind of multi-field entry the
// Models section's provider creation does, and is left for the same
// honest, explicit reason: "n" reports a notice, not a silent no-op.
type agentsSection struct {
	store         ports.AgentSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []ports.AgentView
	cursor int
	notice string
}

func newAgentsSection(store ports.AgentSettings) *agentsSection { return &agentsSection{store: store} }

func (s *agentsSection) Title() string { return "Agents" }

func (s *agentsSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *agentsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

func (s *agentsSection) rebuild() {
	s.rows = s.store.Agents()
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

type agentsSavedMsg struct{}
type agentsFailedMsg struct{ message string }

func awaitAgentsSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return agentsFailedMsg{message: last.Message}
		}
		return agentsSavedMsg{}
	}
}

func (s *agentsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case agentsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case agentsFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *agentsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.store == nil || len(s.rows) == 0 {
		return s, nil
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		s.notice = ""
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
		s.notice = ""
	case "x":
		return s.remove()
	case "n", "enter":
		s.notice = "creating or editing an agent is not available in this build yet"
	}
	return s, nil
}

func (s *agentsSection) remove() (section, tea.Cmd) {
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.RemoveAgent{Name: s.rows[s.cursor].Name})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitAgentsSave(handle)
}

// rowGap is the space between columns in an aligned settings list,
// matching the two-space rhythm render.Header already uses between its
// own meta and state columns.
const rowGap = 2

func (s *agentsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Agents is unavailable.")
	}
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		cells[i] = s.renderCells(row)
	}
	aligned := render.Columns(rowGap, cells)

	var b []byte
	for i, line := range aligned {
		marker := "  "
		if i == s.cursor {
			marker = "> "
		}
		b = append(b, (marker + line)...)
		b = append(b, '\n')
	}
	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

// renderCells draws one agent's row as separately-aligned cells: name,
// description, model binding, tool count, and the prompt's LENGTH only
// - SystemPromptChars, never the text (settings-screen.md §5, the same
// "(set, N chars)" shape internal/cli/config_cmd.go already uses).
// render.Columns pads each cell to its column's widest value across
// every row, replacing the fixed "  " join that left ragged columns
// whenever names or descriptions varied in width.
func (s *agentsSection) renderCells(row ports.AgentView) []string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := fg.Bold(true).Render(row.Name)
	desc := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(row.Description)
	model := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(strings.TrimSpace(row.Provider + "/" + row.Model))
	tools := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d tools", len(row.Tools)))
	prompt := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("prompt %d chars", row.SystemPromptChars))
	return []string{name, desc, model, tools, prompt}
}

func (s *agentsSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsDelete}
}
