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
// The cancel runs in the returned tea.Cmd, never inline - see
// subagentTaskCancelResultMsg below for why.
//
// Split out of keys.go: it stays under the LOC cap.
func (s Screen) cancelSelectedSubagentTask() (app.Screen, tea.Cmd) {
	a, isAgent := s.panel.selectedAgent()
	if !isAgent || s.threads == nil {
		return s, nil
	}
	threads, callID, name := s.threads, a.ID, a.displayName()
	return s, func() tea.Msg {
		ok, err := threads.CancelSubagentTask(callID)
		return subagentTaskCancelResultMsg{name: name, ok: ok, err: err}
	}
}

// subagentTaskCancelResultMsg carries one CancelSubagentTask outcome back
// to the update loop.
//
// The cancel must not run inline in the key handler: that handler is
// reached from Update, which bubbletea runs on its single event loop, and
// CancelSubagentTask reaches the coordinator's per-task cancel - which
// blocks for up to its whole wait budget (seconds) waiting for the task to
// unwind. Inline, that froze rendering AND every message, ctrl+c included.
//
// name is the row's display name captured at key-press time, so the notice
// names the task the user acted on even if the selection has since moved.
type subagentTaskCancelResultMsg struct {
	name string
	ok   bool
	err  error
}

// handleSubagentTaskCancelResult emits the statusline notice for one
// finished cancel attempt: the error text on failure, "cancelling <name>"
// on success, and nothing at all on a miss (no live coordinator route -
// there is nothing to report).
func (s Screen) handleSubagentTaskCancelResult(msg subagentTaskCancelResultMsg) (app.Screen, tea.Cmd) {
	if msg.err != nil {
		s.statusline.Notice("cancel subagent task failed: " + msg.err.Error())
		return s, nil
	}
	if msg.ok {
		s.statusline.Notice("cancelling " + msg.name)
	}
	return s, nil
}
