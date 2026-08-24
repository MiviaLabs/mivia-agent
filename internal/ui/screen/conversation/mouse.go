package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// handleWheel applies one mouse-wheel notch. Wheel events scroll the
// conversation; CockpitScrollLines is the multiplier: terminals
// disagree on how many events one physical notch produces, and some
// amplify while others send exactly one
// (docs/design/cockpit-research.md rule 6.6).
func (s Screen) handleWheel(msg tea.MouseWheelMsg) (app.Screen, tea.Cmd) {
	step := uikitconfig.CockpitScrollLines
	if msg.Button == tea.MouseWheelUp {
		step = -step
	}
	if s.approval.Active() {
		// The approval prompt is modal for keys; the wheel follows it,
		// scrolling the diff preview instead of the transcript behind
		// it.
		s.approval = s.approval.ScrollBy(step)
		return s, nil
	}
	// The content dialog covers the chat column; scrolling a transcript
	// the user cannot see acts on something invisible (the same rule
	// that dismisses overlays on any key). The dialog scrolls by
	// keyboard only.
	if s.panel.dialog {
		return s, nil
	}
	s.transcript = s.transcript.ScrollBy(step)
	return s, nil
}
