// Package statusline renders the permanent status row above the
// composer: the brand mark in the turn's state, the activity label,
// and the elapsed time while a turn is in flight, and the row is
// reserved (and shows the keymap hint) when it is not. The earlier
// inline-first "no permanent bar" decision was reversed for the
// cockpit: a fixed row costs nothing that moves and never reflows the
// transcript when it changes.
package statusline

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/mark"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// labelStates maps the turn's activity label to the brand mark's
// state (mock view 18). Labels with no entry keep the subtle line; the
// mark's motion, not its colour, carries the activity.
var labelStates = map[string]mark.State{
	"thinking": mark.Thinking,
	"waiting":  mark.Waiting,
	"pending":  mark.Pending,
	"running":  mark.Running,
	"failed":   mark.Failed,
	"done":     mark.Done,
}

// Model owns no timer of its own beyond the spinner tick, and that tick
// only runs while a turn is active (Start/Stop bracket it).
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	active  bool
	label   string
	detail  string
	started time.Time
	frame   int
	mark    mark.Model

	// notice is a one-line message shown INSTEAD of the turn line, and
	// only until the next turn starts. It carries the outcome of an
	// action that has no other visible result: a clipboard write, or the
	// armed second half of a two-press quit.
	notice string
}

// TickMsg advances the spinner frame.
type TickMsg struct{}

// tickCmd sleeps one mark interval and yields THIS package's TickMsg:
// the screen and its tests speak statusline.TickMsg, while the mark's
// own tick stays internal to it. Wrapping (rather than returning
// mark.TickCmd directly) also keeps the Cmd re-callable, which tea.Tick
// alone is not: its timer channel is one-shot.
func tickCmd() tea.Cmd {
	return func() tea.Msg {
		mark.TickCmd()()
		return TickMsg{}
	}
}

// New returns an inactive Model.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// Start arms the status line for a new turn with the given state label
// (e.g. "thinking", "running tool") and returns the Cmd that starts the
// spinner clock.
func (m *Model) Start(label string, now time.Time) tea.Cmd {
	m.notice = ""
	m.active = true
	m.label = label
	m.detail = ""
	m.started = now
	m.frame = 0
	m.mark = mark.New(m.Theme, m.Tier, stateFor(label))
	return tickCmd()
}

// stateFor resolves a label to a mark state, defaulting to thinking:
// an unknown activity label still means the agent is working.
func stateFor(label string) mark.State {
	if st, ok := labelStates[label]; ok {
		return st
	}
	return mark.Thinking
}

// SetLabel updates the label of an already-active turn without
// resetting the elapsed-time clock. The mark follows, restarting its
// cycle, because a new activity is a new animation even mid-turn.
func (m *Model) SetLabel(label string) {
	m.label = label
	m.detail = ""
	m.mark.SetState(stateFor(label))
}

// SetDetail sets the specific activity detail shown beside the label -
// wireframes-panes.md section 9's "- running  <detail>   12s" shape,
// e.g. a tool's name and arguments. Call it after SetLabel: SetLabel
// itself clears detail, so a label change with no new detail (back to
// "thinking" once a tool call ends) never keeps showing the PREVIOUS
// tool's detail.
func (m *Model) SetDetail(detail string) { m.detail = detail }

// Stop clears the status line (turn ended or was cancelled).
func (m *Model) Stop() { m.active = false }

// SetTheme re-resolves the mark's colours after a theme change.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	m.mark.Theme, m.mark.Tier = t, tier
}

// Notice shows a one-line message until the next turn starts.
//
// A clipboard write is the case that needs it. tea.SetClipboard emits
// OSC 52, which VTE and Terminal.app ignore silently, so the line states
// what was ATTEMPTED. It never claims the paste buffer now holds the
// text, because this process cannot find that out.
func (m *Model) Notice(text string) { m.notice = text }

// ClearNotice removes any pending notice.
func (m *Model) ClearNotice() { m.notice = "" }

// Active reports whether the line draws anything.
func (m Model) Active() bool { return m.active || m.notice != "" }

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if _, ok := msg.(TickMsg); !ok || !m.active {
		return m, nil
	}
	m.frame++
	next, cmd := m.mark.Update(mark.TickMsg{})
	m.mark = next
	return m, cmd
}

// View renders the line as of now. now is a parameter rather than
// time.Now() so the render stays deterministic for golden tests.
func (m Model) View(now time.Time) string {
	if m.notice != "" {
		return render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(m.notice)
	}
	if !m.active {
		return ""
	}
	elapsed := now.Sub(m.started).Round(time.Second)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	line := m.mark.View() + subtle.Render(" ") + subtle.Render(m.label)
	if m.detail != "" {
		line += subtle.Render("  " + m.detail)
	}
	return line + subtle.Render(fmt.Sprintf("  %s", elapsed))
}
