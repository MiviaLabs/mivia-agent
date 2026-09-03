package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
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

	// on is the statusline the notice belongs on. The local key path leaves
	// it nil, because the session it cancels is by definition the one on
	// screen. A REMOTE cancel can name a backgrounded session, and its
	// notice - above all the error text - has to appear against the session
	// the instruction targeted, not against whichever one happens to be
	// foreground when the coordinator finally answers.
	on *statusline.Model
}

// noticeOn writes to the targeted statusline when the sender named one, and
// to the foreground otherwise.
//
// The receiver is a POINTER on purpose. s.statusline is a value field, so a
// value receiver would take the address of a COPY's field and the notice
// would be written to a Screen that is immediately discarded - the callers
// here persist their mutation only by returning s.
func (s *Screen) noticeOn(on *statusline.Model, text string) {
	if on != nil {
		on.Notice(text)
		return
	}
	s.statusline.Notice(text)
}

// handleSubagentTaskCancelResult emits the statusline notice for one
// finished cancel attempt: the error text on failure, "cancelling <name>"
// on success, and nothing at all on a miss (no live coordinator route -
// there is nothing to report).
func (s Screen) handleSubagentTaskCancelResult(msg subagentTaskCancelResultMsg) (app.Screen, tea.Cmd) {
	if msg.err != nil {
		s.noticeOn(msg.on, "cancel subagent task failed: "+msg.err.Error())
		return s, nil
	}
	if msg.ok {
		s.noticeOn(msg.on, "cancelling "+msg.name)
	}
	return s, nil
}
