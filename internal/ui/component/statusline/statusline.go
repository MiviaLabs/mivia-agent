// Package statusline renders the transient status line attached to the
// active turn: a spinner, elapsed time, and a short state label. The
// inline-first design (build spec section 3.1) has no permanent status
// bar, so this line only exists while a turn is in flight.
package statusline

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

var spinnerFrames = []string{"|", "/", "-", "\\"}

// Model owns no timer of its own beyond the spinner tick, and that tick
// only runs while a turn is active (Start/Stop bracket it).
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	active  bool
	label   string
	started time.Time
	frame   int

	// notice is a one-line message shown INSTEAD of the turn line, and
	// only until the next turn starts. It carries the outcome of an
	// action that has no other visible result: a clipboard write, or the
	// armed second half of a two-press quit.
	notice string
}

// TickMsg advances the spinner frame.
type TickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second/uikitconfig.SpinnerFPS, func(time.Time) tea.Msg { return TickMsg{} })
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
	m.started = now
	m.frame = 0
	return tickCmd()
}

// SetLabel updates the label of an already-active turn without
// resetting the elapsed-time clock.
func (m *Model) SetLabel(label string) { m.label = label }

// Stop clears the status line (turn ended or was cancelled).
func (m *Model) Stop() { m.active = false }

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
	m.frame = (m.frame + 1) % len(spinnerFrames)
	return m, tickCmd()
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
	style := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	return style.Render(fmt.Sprintf("%s %s  %s", spinnerFrames[m.frame], m.label, elapsed))
}
