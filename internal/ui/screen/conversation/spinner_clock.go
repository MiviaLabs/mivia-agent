package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
)

// This file owns the ONE spinner clock: the arm/disarm pair, the tick
// handler that is its only re-arm point, and the activity predicates that
// decide whether it keeps running. See the Screen.tickArmed field for the
// defect this structure exists to prevent.

// armTick returns the spinner-clock Cmd only when no clock is already
// running, and nil otherwise. It is the single entry point for starting
// the animation: see the tickArmed field for why an unguarded
// statusline.TickCmd() call compounds instead of replacing.
//
// The flag is cleared by handleStatuslineTick, which owns the other half
// of the cycle - each delivered tick either re-arms (staying one clock)
// or lets the loop lapse.
func (s Screen) armTick() tea.Cmd {
	if s.tickArmed == nil {
		// A zero-value Screen (tests constructing the struct directly)
		// has no shared flag; a lone clock is still correct there.
		return statusline.TickCmd()
	}
	if *s.tickArmed {
		return nil
	}
	*s.tickArmed = true
	return statusline.TickCmd()
}

// disarmTick records that no spinner clock is in flight, so the next
// armTick starts one.
func (s Screen) disarmTick() {
	if s.tickArmed != nil {
		*s.tickArmed = false
	}
}

// handleStatuslineTick advances the statusline spinner frame and continues
// ticking while turns or subagents are active.
//
// This is the ONLY place the clock re-arms itself. The delivered tick
// consumed the in-flight clock, so the flag drops first and the re-arm
// goes back through armTick: the loop stays exactly one clock, and when
// nothing is active it lapses and leaves the flag clear for the next
// turn or dispatch.
func (s Screen) handleStatuslineTick(msg statusline.TickMsg) (app.Screen, tea.Cmd) {
	if s.embedded {
		// An embedded thread screen is driven by its parent's forwarded
		// ticks (forwardSharedMsg) and its Cmd is discarded there. It must
		// therefore neither consume nor re-arm the shared clock: doing so
		// would clear the flag while the parent's tick is still in flight,
		// and the next arm would start a SECOND clock. It only advances its
		// own frame.
		next, _ := s.statusline.Update(msg)
		s.statusline = next
		s.transcript.SetSpinnerFrame(s.statusline.Frame())
		return s, nil
	}
	s.disarmTick()
	next, cmd := s.statusline.Update(msg)
	s.statusline = next
	s.transcript.SetSpinnerFrame(s.statusline.Frame())
	if cmd != nil {
		// statusline.Update re-armed on its own; that Cmd IS the one
		// clock, so record it rather than adding a second.
		if s.tickArmed != nil {
			*s.tickArmed = true
		}
	} else if s.panel.activeAgentCount() > 0 || s.hasActiveSession() {
		cmd = s.armTick()
	}
	s.forwardSharedMsg(msg)
	return s, cmd
}

// hasActiveSession reports whether the foreground session or any background session
// has in-flight turns, active statuslines, or active subagents.
//
// It asks statusline.Animating, never Active: Active is true for a bare
// notice ("copied the block"), which outlives its turn and would pin the
// clock on forever with nothing to animate.
func (s Screen) hasActiveSession() bool {
	if s.active != nil || s.statusline.Animating() || s.panel.activeAgentCount() > 0 {
		return true
	}
	// The embedded thread screen no longer keeps the clock alive itself
	// (see handleStatuslineTick), so its activity has to count here or an
	// open thread's turn would animate for exactly one frame.
	if s.thread != nil && (s.thread.active != nil || s.thread.statusline.Animating()) {
		return true
	}
	return s.hasActiveBackgroundSession()
}

// hasActiveBackgroundSession reports whether any session in s.sessions has an
// in-flight turn, active statusline, or active subagents.
func (s Screen) hasActiveBackgroundSession() bool {
	for _, st := range s.sessions {
		if st != nil && (st.active != nil || st.statusline.Animating() || st.panel.activeAgentCount() > 0) {
			return true
		}
	}
	return false
}
