package mark

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// State is what the mark says about the turn.
type State int

const (
	Idle State = iota
	Waiting
	Thinking
	Streaming
	Running
	Pending
	Failed
	Done
)

// Word is the state as a word, for screen-reader output: the mock's
// rule is that a screen reader gets the state word only, never the
// glyph or the animation ("thinking, 4.2 seconds, escape to cancel").
func (s State) Word() string {
	switch s {
	case Idle:
		return "idle"
	case Waiting:
		return "waiting"
	case Thinking:
		return "thinking"
	case Streaming:
		return "writing"
	case Running:
		return "running"
	case Pending:
		return "pending"
	case Failed:
		return "failed"
	case Done:
		return "done"
	}
	return "idle"
}

// Animated reports whether the state moves. Static means "not working".
func (s State) Animated() bool {
	switch s {
	case Waiting, Thinking, Streaming, Running, Pending:
		return true
	}
	return false
}

// waitDivisor is how much slower waiting blinks than thinking rotates:
// a slow mark reads as "blocked on someone else" (mock view 18).
const waitDivisor = 4

// roles maps each state to its primary theme role. Yellow (RoleWarning)
// is strictly reserved for states requiring human attention: Waiting and
// Pending. Autonomous work (Thinking, Running) renders monochrome (RoleFG),
// and Streaming retains its success status role (RoleSuccess).
var roles = map[State]theme.Role{
	Thinking:  theme.RoleFG,
	Streaming: theme.RoleSuccess,
	Running:   theme.RoleFG,
	Waiting:   theme.RoleWarning,
	Idle:      theme.RoleFGSubtle,
	Pending:   theme.RoleWarning,
	Failed:    theme.RoleDanger,
	Done:      theme.RoleSuccess,
}

// Model is the mark plus its tick. The tick only runs while the state
// animates, mirroring statusline's Start/Stop bracketing.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	state State
	frame int
}

// TickMsg advances an animated mark's frame.
type TickMsg struct{}

// TickCmd returns the next-tick command. One interval for every state:
// waiting goes slower by dividing its frame index, not its clock, so
// all marks on screen share one ticker.
func TickCmd() tea.Cmd {
	return tea.Tick(time.Second/uikitconfig.SpinnerFPS, func(time.Time) tea.Msg { return TickMsg{} })
}

// New returns a Model in state s. A static state needs no ticker.
func New(t theme.Theme, tier theme.Tier, s State) Model {
	return Model{Theme: t, Tier: tier, state: s}
}

// SetState changes the state, resetting the frame so a rotation never
// resumes mid-cycle from a stale position.
func (m *Model) SetState(s State) {
	if m.state != s {
		m.frame = 0
	}
	m.state = s
}

// State reports the current state.
func (m Model) State() State { return m.state }

// Update advances the frame on a tick. Static states ignore ticks:
// their glyph never changes, so no clock runs for them.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if _, ok := msg.(TickMsg); !ok || !m.state.Animated() {
		return m, nil
	}
	m.frame++
	return m, TickCmd()
}

// pulseGlyph returns the animated glyph for an active frame.
// TrueColor/256Color/16 tiers use an 8-frame diamond braille pulse;
// ASCII/NoTTY tiers use a 3-symbol cycle (. + * * + * + .) matching
// the 8 frames so character positions stay identical.
func pulseGlyph(phase int, isASCII bool) rune {
	if isASCII {
		switch phase {
		case 2, 3, 5:
			return '*'
		case 1, 4, 6:
			return '+'
		default:
			return '.'
		}
	}
	switch phase {
	case 0, 7:
		return '⠶'
	case 1, 6:
		return '⠛'
	case 2, 5:
		return '⠿'
	case 3:
		return '⣿'
	case 4:
		return '⣶'
	default:
		return '⠶'
	}
}

// Glyph is the mark's lead cell, unstyled.
func (m Model) Glyph() rune {
	if m.state == Idle || m.state == Done {
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			return '<'
		}
		return '⬖'
	}
	if m.state == Failed {
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			return 'X'
		}
		return '◆'
	}
	isASCII := m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY
	f := m.frame
	if m.state == Waiting {
		f = m.frame / waitDivisor
	}
	phase := f % 8
	if phase < 0 {
		phase += 8
	}
	return pulseGlyph(phase, isASCII)
}

// View renders the mark: single brand glyph for idle/done/failed, and
// an animated single-cell braille pulse with role shading for active states.
func (m Model) View() string {
	if m.state == Idle {
		glyph := '⬖'
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = '<'
		}
		return render.Role(m.Theme, m.Tier, roles[m.state]).Render(string(glyph))
	}
	if m.state == Failed {
		glyph := '◆'
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = 'X'
		}
		return render.Role(m.Theme, m.Tier, theme.RoleDanger).Render(string(glyph))
	}
	if m.state == Done {
		glyph := '⬖'
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = '<'
		}
		return render.Role(m.Theme, m.Tier, theme.RoleSuccess).Render(string(glyph))
	}

	return m.renderPulse()
}

func (m Model) renderPulse() string {
	isASCII := m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY

	f := m.frame
	if m.state == Waiting {
		f = m.frame / waitDivisor
	}

	phase := f % 8
	if phase < 0 {
		phase += 8
	}

	glyph := pulseGlyph(phase, isASCII)
	role := roles[m.state]
	return render.Role(m.Theme, m.Tier, role).Render(string(glyph))
}
