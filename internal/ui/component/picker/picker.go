// Package picker is a generic, minimal list picker: items, a cursor, and
// a substring filter. It is the shared building block a later alt-screen
// theme picker will use; no other consumer exists yet, so it stays
// intentionally small - no fuzzy ranking, no multi-select.
package picker

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Model is a plain list picker over a fixed item slice.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	items  []string
	filter string
	cursor int
}

// New returns a Model over items with the cursor on the first row.
func New(t theme.Theme, tier theme.Tier, items []string) Model {
	return Model{Theme: t, Tier: tier, items: items}
}

// Selected returns the currently highlighted item and whether the
// (filtered) list is non-empty.
func (m Model) Selected() (string, bool) {
	visible := m.visible()
	if m.cursor < 0 || m.cursor >= len(visible) {
		return "", false
	}
	return visible[m.cursor], true
}

func (m Model) visible() []string {
	if m.filter == "" {
		return m.items
	}
	out := make([]string, 0, len(m.items))
	needle := strings.ToLower(m.filter)
	for _, it := range m.items {
		if strings.Contains(strings.ToLower(it), needle) {
			out = append(out, it)
		}
	}
	return out
}

// SelectMsg is emitted when the user confirms a selection with Enter.
type SelectMsg struct{ Item string }

// CancelMsg is emitted when the user aborts with Esc.
type CancelMsg struct{}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.visible())-1 {
			m.cursor++
		}
	case "enter":
		if item, ok := m.Selected(); ok {
			return m, func() tea.Msg { return SelectMsg{Item: item} }
		}
	case "esc":
		return m, func() tea.Msg { return CancelMsg{} }
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
		}
	default:
		if key.Text != "" {
			m.filter += key.Text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m Model) View() string {
	visible := m.visible()
	var b strings.Builder
	for i, it := range visible {
		style, prefix := render.Role(m.Theme, m.Tier, theme.RoleFG), "  "
		if i == m.cursor {
			style = render.WithBg(style, m.Theme, m.Tier, theme.RoleBGSelection)
			prefix = "> "
		}
		b.WriteString(style.Render(prefix + it))
		b.WriteByte('\n')
	}
	if m.filter != "" {
		b.WriteString(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("/" + m.filter))
	}
	return strings.TrimRight(b.String(), "\n")
}
