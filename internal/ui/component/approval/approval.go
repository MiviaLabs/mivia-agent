// Package approval renders one pending tool-approval request inline and
// turns keypresses into a ports.Decision.
package approval

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Model holds at most one pending request: a new SetRequest replaces
// whatever was pending. This is the inline prompt the build spec calls
// for (uikit/config.ApprovalDefaultInline), not a queue-visualising
// dialog - promotion to a full alt-screen dialog is a later screen, not
// this component's job.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	active *uievent.ToolPendingBody
}

// DecisionMsg is emitted when the user resolves the active request.
type DecisionMsg struct {
	ToolCallID string
	Decision   ports.Decision
}

// New returns a Model with no request pending.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// SetRequest arms the prompt for a new pending tool call.
func (m *Model) SetRequest(b uievent.ToolPendingBody) { m.active = &b }

// Active reports whether a request is currently awaiting a decision.
func (m Model) Active() bool { return m.active != nil }

// Update ignores every Msg except a key press while a request is active.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok || m.active == nil {
		return m, nil
	}
	var decision ports.Decision
	switch key.String() {
	case "y", "enter":
		decision = ports.DecisionOnce
	case "a":
		decision = ports.DecisionAlways
	case "n", "esc":
		decision = ports.DecisionDeny
	case "d":
		decision = ports.DecisionDenyAlways
	default:
		return m, nil
	}
	id := m.active.ToolCallID
	m.active = nil
	return m, func() tea.Msg { return DecisionMsg{ToolCallID: id, Decision: decision} }
}

// View renders the prompt, or "" when nothing is pending.
func (m Model) View() string {
	if m.active == nil {
		return ""
	}
	title := render.Role(m.Theme, m.Tier, theme.RoleWarning).Bold(true).Render(
		"approve " + m.active.Name + " " + render.FormatArgs(m.active.Args))
	hint := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
		"[y] once  [a] always  [n] deny  [d] deny always")
	return title + "\n" + hint
}
