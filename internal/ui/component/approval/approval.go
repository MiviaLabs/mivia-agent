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

// Clear dismisses the prompt without emitting a decision (e.g. tool started out-of-band or turn ended).
func (m *Model) Clear() { m.active = nil }

// Active reports whether a request is currently awaiting a decision.
func (m Model) Active() bool { return m.active != nil }

// Update ignores every Msg except a key press while a request is active.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok || m.active == nil {
		return m, nil
	}
	// Key set is wireframes-panes.md section 7, exactly: o once, a always,
	// d deny, D deny always. Enter takes once, Esc is deny.
	//
	// "d" is deny-ONCE. An earlier version of this file mapped "d" to
	// DecisionDenyAlways, so a user pressing d granted a standing
	// session-wide denial they did not ask for. Keep d and D distinct.
	var decision ports.Decision
	switch key.String() {
	case "o", "enter":
		decision = ports.DecisionOnce
	case "a":
		decision = ports.DecisionAlways
	case "d", "esc":
		decision = ports.DecisionDeny
	case "D", "shift+d":
		// Both spellings: a terminal sending Text="D" reports "D", but a
		// key event carrying only Code='d' plus ModShift reports
		// "shift+d". Verified against charm.land/bubbletea/v2 Key.String.
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
	// The hint states the complete truth for this state: every key listed
	// works, and no key that works is omitted. "v view diff" from the
	// design is deliberately absent until a diff view exists to open.
	hint := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
		"o once    a always    d deny    D deny always")
	// RoleBorderFocus, not RoleBorder: the prompt carries state (a tool is
	// blocked on this answer), so its border must meet the 3:1 contrast
	// the plain decorative border is exempt from.
	return render.Bordered(m.Theme, m.Tier, theme.RoleBorderFocus, title+"\n"+hint)
}
