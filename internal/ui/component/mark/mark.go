package mark

import (
	"strings"
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

// roles maps each state to its primary theme role.
var roles = map[State]theme.Role{
	Thinking:  theme.RoleInfo,
	Streaming: theme.RoleSuccess,
	Running:   theme.RoleInfo,
	Waiting:   theme.RoleFGSubtle,
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
	phase := f % 6
	if phase < 0 {
		phase += 6
	}
	switch phase {
	case 0, 1:
		if isASCII {
			return '*'
		}
		return '✦'
	case 2, 5:
		if isASCII {
			return '+'
		}
		return '✧'
	default:
		if isASCII {
			return '.'
		}
		return '·'
	}
}

// View renders the mark: single brand glyph for idle/done/failed, and
// an animated 3-cell colorful aurora wave for active states.
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

	return m.renderAuroraWave()
}

func (m Model) renderAuroraWave() string {
	isASCII := m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY

	var peakRole, midRole, valleyRole theme.Role
	switch m.state {
	case Thinking:
		peakRole = theme.RoleInfo
		midRole = theme.RoleAccent
		valleyRole = theme.RoleFGSubtle
	case Running:
		peakRole = theme.RoleInfo
		midRole = theme.RoleAccent
		valleyRole = theme.RoleFGSubtle
	case Streaming:
		peakRole = theme.RoleSuccess
		midRole = theme.RoleInfo
		valleyRole = theme.RoleFGSubtle
	case Waiting, Pending:
		peakRole = theme.RoleWarning
		midRole = theme.RoleFGSubtle
		valleyRole = theme.RoleFGSubtle
	default:
		peakRole = theme.RoleAccent
		midRole = theme.RoleFGSubtle
		valleyRole = theme.RoleFGSubtle
	}

	f := m.frame
	if m.state == Waiting {
		f = m.frame / waitDivisor
	}

	var sb strings.Builder
	for c := 0; c < 3; c++ {
		if c > 0 {
			sb.WriteString(" ")
		}
		phase := (f - c) % 6
		if phase < 0 {
			phase += 6
		}

		var glyph rune
		var role theme.Role

		switch phase {
		case 0, 1:
			if isASCII {
				glyph = '*'
			} else {
				glyph = '✦'
			}
			role = peakRole
		case 2, 5:
			if isASCII {
				glyph = '+'
			} else {
				glyph = '✧'
			}
			role = midRole
		default:
			if isASCII {
				glyph = '.'
			} else {
				glyph = '·'
			}
			role = valleyRole
		}

		sb.WriteString(render.Role(m.Theme, m.Tier, role).Render(string(glyph)))
	}
	return sb.String()
}
