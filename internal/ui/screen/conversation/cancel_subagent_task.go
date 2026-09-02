package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
)

// cancelSelectedSubagentTask cancels the ONE coordinator task backing the
// selected files-panel subagent row, leaving siblings and the parent run
// untouched. Silent no-op when nothing is selected, SubagentThreads isn't
// wired, or the row's callID has no live coordinator route (a terminal
// run, or a row rebuilt from persisted history, which carries no live
// coordinator identity).
//
// Calls ports.SubagentThreads.CancelSubagentTask, NOT TurnHandle.Cancel()
// / ActiveTurn().Cancel() (those only detach a UI listener) - this reaches
// the coordinator task itself.
//
// The live route table behind that call is populated at dispatch time by
// internal/cliorchestrate, which publishes each spawned task's
// (coordinator, runID, taskID) into internal/uiadapter through
// uiadapter.SubagentTaskRouteRegistrar (wired in internal/newtui). The row
// id this passes as callID is the namespaced "<tool call id>:<task id>"
// dispatchTaskIDsAndNames builds, which is exactly the key that path
// registers under.
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
