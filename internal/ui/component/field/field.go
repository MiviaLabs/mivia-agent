// Package field is one editable settings row: a label plus either free
// text (KindText, wrapping bubbles/textinput) or a cycled value from a
// closed set (KindChoice, no textinput at all - so an invalid value is
// unreachable, not merely rejected). See
// docs/design/settings-screen.md §7: this is the only editing
// primitive the settings screen has; there is no separate multi-field
// form type until a second section needs one.
package field

import (
	"strconv"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Kind selects a field's editing shape.
type Kind int

const (
	KindText Kind = iota
	KindChoice
)

// Model is one field. Theme/Tier are exported like picker.Model's own
// fields; SetTheme exists anyway (not a bare assignment) because
// KindText's embedded textinput carries hard-coded styles bubbles ships
// with, and forgetting to restyle it is exactly the composer.go:79-96
// bug this package must not repeat.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	label string
	kind  Kind

	input textinput.Model // KindText only

	choices  []string // KindChoice only
	choiceAt int

	validate func(string) error
	focused  bool
	width    int
}

// New builds a field. width is the full row width available for the
// value; KindText clamps its input to it, KindChoice ignores it (a
// single value never wraps).
func New(t theme.Theme, tier theme.Tier, label string, kind Kind, width int) Model {
	ti := textinput.New()
	m := Model{label: label, kind: kind, input: ti}
	m.SetTheme(t, tier)
	m.SetWidth(width)
	return m
}

// SetTheme restyles the field, including KindText's embedded
// textinput - see the package doc for why this cannot be a bare field
// assignment.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	st := textinput.DefaultStyles(t.Dark)
	st.Focused.Text = render.Role(t, tier, theme.RoleFG)
	st.Focused.Placeholder = render.Role(t, tier, theme.RoleFGSubtle)
	st.Blurred.Text = render.Role(t, tier, theme.RoleFGMuted)
	st.Blurred.Placeholder = render.Role(t, tier, theme.RoleFGSubtle)
	if s := t.Resolve(theme.RoleAccent, tier); s.Hex != "" {
		st.Cursor.Color = lipgloss.Color(s.Hex)
	} else if s.ANSI16 >= 0 {
		st.Cursor.Color = lipgloss.Color(strconv.Itoa(s.ANSI16))
	} else {
		st.Cursor.Color = nil
	}
	m.input.SetStyles(st)
}

func (m *Model) SetWidth(w int) {
	m.width = w
	m.input.SetWidth(w)
}

// SetValidate injects the field's validation rule. A KindEnvName-style
// variant is not a separate Kind (settings-screen.md §7's cut): the
// POSIX env-var check, the positive-int check, and every other rule
// this screen needs are a few lines each, injected here.
func (m *Model) SetValidate(f func(string) error) { m.validate = f }

// SetChoices sets a KindChoice field's closed value set and starts the
// cursor on active (or the first choice if active matches none).
func (m *Model) SetChoices(choices []string, active string) {
	m.choices = choices
	for i, c := range choices {
		if c == active {
			m.choiceAt = i
			return
		}
	}
	m.choiceAt = 0
}

// SetValue sets a KindText field's starting text.
func (m *Model) SetValue(v string) { m.input.SetValue(v) }

// Value is the field's current value: the input text for KindText, the
// selected choice for KindChoice.
func (m Model) Value() string {
	if m.kind == KindChoice {
		if len(m.choices) == 0 {
			return ""
		}
		return m.choices[m.choiceAt]
	}
	return m.input.Value()
}

// Cycle moves a KindChoice field to the next (delta>0) or previous
// (delta<0) value, wrapping. A no-op on KindText.
func (m *Model) Cycle(delta int) {
	if m.kind != KindChoice || len(m.choices) == 0 {
		return
	}
	n := len(m.choices)
	m.choiceAt = ((m.choiceAt+delta)%n + n) % n
}

// Focus arms the field for input. KindText forwards to textinput's own
// Focus (which returns the cursor-blink Cmd); KindChoice has nothing to
// focus beyond the marker its owner draws.
func (m *Model) Focus() tea.Cmd {
	m.focused = true
	if m.kind == KindText {
		return m.input.Focus()
	}
	return nil
}

func (m *Model) Blur() {
	m.focused = false
	m.input.Blur()
}

func (m Model) Focused() bool { return m.focused }

// Validate runs the injected rule against the current value. A field
// with no injected rule is always valid.
func (m Model) Validate() error {
	if m.validate == nil {
		return nil
	}
	return m.validate(m.Value())
}

// Update forwards a key to the embedded textinput while a KindText
// field is focused. KindChoice never reaches here: its owner drives
// Cycle directly, since there is no free-text buffer to route keys
// into.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.kind != KindText || !m.focused {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders "label  value", the value reverse-videoed while
// focused - the same marker-free focus convention picker.View() uses,
// so a field row and a picker row read as the same kind of thing.
func (m Model) View() string {
	label := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(m.label)
	var value string
	switch {
	case m.kind == KindText:
		value = m.input.View()
	case m.focused:
		value = render.WithBg(render.Role(m.Theme, m.Tier, theme.RoleFG), m.Theme, m.Tier, theme.RoleBGSelection).Render(m.Value())
	default:
		value = render.Role(m.Theme, m.Tier, theme.RoleFG).Render(m.Value())
	}
	return label + "  " + value
}
