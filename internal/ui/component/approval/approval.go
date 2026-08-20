// Package approval renders one pending tool-approval request inline and
// turns keypresses into a ports.Decision.
package approval

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
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

	// offset is the first rendered diff line the preview window shows.
	// It is only meaningful while active carries a diff, and it never
	// changes the prompt's height (see View).
	offset int

	// width is the terminal width. The bordered box is padded to it and
	// every diff line is clipped to it, so the border keeps one fixed
	// size while the window scrolls instead of breathing with whichever
	// line is widest (ux-rules.md rule 2.7: moving chrome reflows the
	// reading). 0 means unsized, for tests and non-terminal renders.
	width int
}

// SetWidth records the terminal width so the box renders at a fixed
// size. Call it on WindowSizeMsg, like the composer.
func (m *Model) SetWidth(w int) { m.width = w }

// DecisionMsg is emitted when the user resolves the active request.
type DecisionMsg struct {
	ToolCallID string
	Decision   ports.Decision
}

// New returns a Model with no request pending.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// SetRequest arms the prompt for a new pending tool call. The scroll
// offset restarts at the top: a new request is a new diff, and a stale
// offset could land the first view halfway through it.
func (m *Model) SetRequest(b uievent.ToolPendingBody) {
	m.active = &b
	m.offset = 0
}

// Clear dismisses the prompt without emitting a decision (e.g. tool started out-of-band or turn ended).
func (m *Model) Clear() {
	m.active = nil
	m.offset = 0
}

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
//
// The diff preview is a FIXED-height window (ApprovalDiffPreviewLines)
// into the full diff at the current scroll offset, with a position row
// ("lines X-Y of Z") when the diff is longer than the window. Fixed
// height matters: scrolling must never change the rows the prompt
// claims, or every wrapped line above it reflows on every keystroke
// (ux-rules.md rule 2.7).
func (m Model) View() string {
	if m.active == nil {
		return ""
	}
	// Every chrome row is clipped to the wrap width before it enters the
	// box: lipgloss wraps a row that does not fit, and a wrapped row is a
	// row Height() does not claim - the box would push into the composer.
	// The title is the realistic breaker (a long command's arguments).
	clip := func(s string) string {
		if m.width > 4 {
			return ansi.Truncate(s, m.width-4, "")
		}
		return s
	}
	label, folded := m.borderLabel()
	// The hint states the complete truth for this state: every key listed
	// works, and no key that works is omitted. The scroll keys live in the
	// keymap's approval context; they are absent here because this line
	// names decision keys only - scrolling has its own position row.
	hint := clip(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
		"o once    a always    d deny    D deny always"))
	var body string
	if !folded {
		// The action did not fit the border row, so it stays where it has
		// always been. Dropping it would hide WHAT is being approved.
		body = clip(render.Role(m.Theme, m.Tier, theme.RoleWarning).Bold(true).
			Render("approve "+m.action())) + "\n"
	}
	if diff := m.diffWindow(); len(diff) > 0 {
		body += strings.Join(diff, "\n") + "\n"
		if m.scrollable() {
			body += clip(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
				fmt.Sprintf("lines %d-%d of %d  up/down:scroll",
					m.offset+1, m.offset+len(diff), m.diffTotal()))) + "\n"
		}
	}
	body += hint
	// RoleBorder, the decorative role, not RoleBorderFocus: the state this
	// prompt carries is now said by the border LABEL, which is text in the
	// warning role and carries its own contrast, so the frame is back to
	// being frame. Same rule as the composer's: the box spans the full
	// terminal width, one fixed size.
	inner := 0
	if m.width > 0 {
		inner = m.width
	}
	return render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleWarning, inner, body, label)
}

// action names what this request would do: the tool and its arguments.
func (m Model) action() string {
	if m.active == nil {
		return ""
	}
	return strings.TrimSpace(m.active.Name + " " + render.FormatArgs(m.active.Args))
}

// borderLabel is the text of the top border row, and whether the action
// went into it.
//
// The state badge always rides there. The action joins it when the row
// can carry the pair whole - a truncated action would name the wrong
// path or the wrong command, which is worse than putting it back in the
// body. The second return is what View and Height agree on, so the row
// count never depends on two separate readings of the same rule.
func (m Model) borderLabel() (string, bool) {
	badge := "⚠ Approval Required"
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		badge = "! Approval Required"
	}
	// Bracketed like the composer's own top-border hint: the two frames
	// sit one above the other, and matching chrome is what makes the
	// prompt read as part of the same surface rather than an alarm.
	action := m.action()
	if action == "" {
		return "[ " + badge + " ]", true
	}
	if full := "[ " + badge + " - " + action + " ]"; render.HintFits(m.width, full) {
		return full, true
	}
	return "[ " + badge + " ]", false
}

// ScrollBy moves the diff preview window by n lines and returns the
// model. The offset clamps to [0, total-window], so scrolling past
// either end is a no-op, never an empty window.
func (m Model) ScrollBy(n int) Model {
	if !m.scrollable() {
		return m
	}
	max := m.diffTotal() - m.windowHeight()
	m.offset = clamp(m.offset+n, 0, max)
	return m
}

// diffTotal is the full rendered line count of the pending diff.
func (m Model) diffTotal() int {
	if m.active == nil || m.active.Diff == nil {
		return 0
	}
	return render.DiffLineCount(*m.active.Diff)
}

// windowHeight is how many diff lines the preview shows at once.
func (m Model) windowHeight() int {
	return min(m.diffTotal(), uikitconfig.ApprovalDiffPreviewLines)
}

// scrollable reports whether the diff is longer than its window.
func (m Model) scrollable() bool {
	return m.diffTotal() > uikitconfig.ApprovalDiffPreviewLines
}

// diffWindow is the styled diff lines currently visible, each clipped to
// the box's inner width: an over-long line would widen (or wrap inside)
// the border and move it while the user scrolls.
func (m Model) diffWindow() []string {
	if m.active == nil || m.active.Diff == nil {
		return nil
	}
	lines := render.DiffLines(m.Theme, m.Tier, *m.active.Diff)
	end := m.offset + m.windowHeight()
	if end > len(lines) {
		end = len(lines)
	}
	window := lines[m.offset:end]
	// Clip to the box's effective wrap width, not the inner width:
	// lipgloss counts the border cells inside Width(), and a line it
	// wraps would add a row Height() does not claim - the box would push
	// into the composer.
	if m.width > 4 {
		wrap := m.width - 4
		for i, ln := range window {
			if ansi.StringWidth(ln) > wrap {
				window[i] = ansi.Truncate(ln, wrap, "")
			}
		}
	}
	return window
}

// Height is the number of terminal rows View() claims, border included,
// so the enclosing screen can reserve them without re-rendering. It is
// 0 when nothing is pending, and CONSTANT while scrolling: the window
// never grows or shrinks with the offset.
func (m Model) Height() int {
	if m.active == nil {
		return 0
	}
	rows := 1 // the decision-key hint
	if _, folded := m.borderLabel(); !folded {
		rows++ // the action, back in the body
	}
	if n := m.windowHeight(); n > 0 {
		rows += n
		if m.scrollable() {
			rows++ // the position row
		}
	}
	return rows + 2 // the border's top and bottom rows
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
