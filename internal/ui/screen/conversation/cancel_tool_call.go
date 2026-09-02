package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// cancelFocusedToolCall cancels the ONE in-flight tool call the focused
// block represents, leaving the rest of the turn (and any concurrent
// sibling tool call) running. It is a silent no-op - never an error, never
// a panic - when nothing is focused, the focused block is not a tool call
// still in the "running" state (transcript.go's handleToolStart is the
// only writer of that state; handleToolEnd replaces it with a terminal
// one), or no turn is active to cancel against.
//
// Split out of keys.go (INV: files stay under the ~500 LOC soft cap / 800
// hard cap) rather than added there, since keys.go was already close to the
// hard cap before this feature.
func (s Screen) cancelFocusedToolCall() (app.Screen, tea.Cmd) {
	block, ok := s.transcript.FocusedBlock()
	if !ok || block.Kind != uievent.KindToolStart || block.Header.State != "running" || block.CallID == "" {
		return s, nil
	}
	if s.active == nil {
		return s, nil
	}
	if s.active.CancelToolCall(block.CallID) {
		s.statusline.Notice("cancelling " + block.Header.Label)
	}
	return s, nil
}
