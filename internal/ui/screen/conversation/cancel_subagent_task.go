package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
)

// cancelSelectedSubagentTask cancels the ONE coordinator task backing the
// selected files-panel subagent row, leaving siblings and the parent run
// untouched. Silent no-op when nothing is selected, SubagentThreads isn't
// wired, or the row's callID has no live coordinator route (terminal, or
// never registered - see KNOWN GAP).
//
// Calls ports.SubagentThreads.CancelSubagentTask, NOT TurnHandle.Cancel()
// / ActiveTurn().Cancel() (those only detach a UI listener) - this reaches
// the coordinator task itself.
//
// KNOWN GAP: the live dispatch path never calls RegisterTaskRoute/
// SetCoordinator, so a subagent's RunID never reaches internal/uiadapter
// today - this key is a no-op for every real row until that handoff
// (internal/cliorchestrate) is wired, a separate follow-up. The mechanism
// itself (CancelTask) and this UI path are both implemented and tested.
//
// Split out of keys.go: it stays under the LOC cap.
func (s Screen) cancelSelectedSubagentTask() (app.Screen, tea.Cmd) {
	a, isAgent := s.panel.selectedAgent()
	if !isAgent || s.threads == nil {
		return s, nil
	}
	ok, err := s.threads.CancelSubagentTask(a.ID)
	if err != nil {
		s.statusline.Notice("cancel subagent task failed: " + err.Error())
		return s, nil
	}
	if ok {
		s.statusline.Notice("cancelling " + a.displayName())
	}
	return s, nil
}
