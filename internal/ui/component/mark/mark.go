// Package mark is the Mivia brand mark: one cell that carries the
// turn's state. The logo is U+2B16 DIAMOND WITH LEFT HALF BLACK, and
// U+2B16..U+2B19 rotate the black half through left/top/right/bottom,
// so the logo animates without ever becoming a different object
// (docs/design/mivia-ui-mock-panes.html view 18).
//
// Four states animate (waiting, thinking, streaming, running) and four
// are static (idle, pending, failed, done). A static mark means the
// agent is not working; that distinction is load-bearing, so nothing
// here may animate a static state or still a moving one.
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
	case Waiting, Thinking, Streaming, Running:
		return true
	}
	return false
}

// waitDivisor is how much slower waiting blinks than thinking rotates:
// a slow mark reads as "blocked on someone else" (mock view 18).
const waitDivisor = 4

// uniFrames and ascFrames are the glyph cycles per state, straight from
// the mock's UNI/ASC tables. ASC is the ASCII/NoTTY fallback, wired
// through theme.Tier like every other tiered renderer.
var uniFrames = map[State][]rune{
	Thinking:  {'⬖', '⬘', '⬗', '⬙'}, // U+2B16..U+2B19
	Running:   {'⬖', '⬘', '⬗', '⬙'},
	Streaming: {'◇', '◈', '◆', '◈'}, // U+25C7/25C8/25C6 fill cycle
	Waiting:   {'⬖', '◇'},
	Idle:      {'⬖'},
	Pending:   {'◈'},
	Failed:    {'◆'},
	Done:      {'⬖'},
}

var ascFrames = map[State][]rune{
	Thinking:  {'<', '^', '>', 'v'},
	Running:   {'<', '^', '>', 'v'},
	Streaming: {'.', 'o', '0', 'o'},
	Waiting:   {'<', ' '},
	Idle:      {'<'},
	Pending:   {'?'},
	Failed:    {'X'},
	Done:      {'<'},
}

// roles maps each state to its theme role, from the mock's ANIM table.
var roles = map[State]theme.Role{
	Thinking:  theme.RoleWarning,
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

// Glyph is the mark's current cell, unstyled.
func (m Model) Glyph() rune {
	frames := uniFrames[m.state]
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		frames = ascFrames[m.state]
	}
	if len(frames) == 0 {
		return ' '
	}
	idx := m.frame % len(frames)
	if m.state == Waiting {
		idx = (m.frame / waitDivisor) % len(frames)
	}
	return frames[idx]
}

// View is the styled single-cell mark.
func (m Model) View() string {
	return render.Role(m.Theme, m.Tier, roles[m.state]).Render(string(m.Glyph()))
}
