package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// cancelFocusedThreadToolCall is cancelFocusedToolCall's counterpart for the
// embedded subagent-thread dialog (thread.go): it cancels the ONE in-flight
// tool call the thread's OWN focused block represents, leaving that
// subagent task, every sibling task, and the parent run untouched.
//
// It is a separate function, not a call to cancelFocusedToolCall: it reads
// the embedded screen's own state (s.thread.transcript, s.threadID), and it
// cancels through a different path - a thread's tool call belongs to a
// coordinator-dispatched subagent task, reached only via
// ports.SubagentThreads.CancelSubagentToolCall (which resolves the task's
// registered ToolCanceler through internal/coordinator), never through
// s.active (the ROOT turn's handle, which has never heard of this call ID).
//
// Reusing the main transcript's "x" binding here is unambiguous:
// threadDialogKey/threadDialogScrollKey are reached ONLY while the panel
// dialog is open on this thread (handlePanelKey's own gate), which fully
// intercepts the key before the main screen's keymap-driven
// transcriptAction dispatch is ever consulted.
func (s Screen) cancelFocusedThreadToolCall() (app.Screen, tea.Cmd) {
	if s.thread == nil || s.threads == nil || s.threadID == "" {
		return s, nil
	}
	block, ok := s.thread.transcript.FocusedBlock()
	if !ok || block.Kind != uievent.KindToolStart || block.Header.State != "running" || block.CallID == "" {
		return s, nil
	}
	threads, threadID, callID, label := s.threads, s.threadID, block.CallID, block.Header.Label
	return s, func() tea.Msg {
		ok, err := threads.CancelSubagentToolCall(threadID, callID)
		return threadToolCallCancelResultMsg{label: label, ok: ok, err: err}
	}
}

// threadToolCallCancelResultMsg carries one CancelSubagentToolCall outcome
// back to the update loop. The call runs in a tea.Cmd rather than inline
// for the same reason cancelSelectedSubagentTask's does: this handler is
// reached from Update, which bubbletea runs on its single event loop, and
// the seam behind it crosses into the coordinator. It is fast today (it
// only invokes a registered function), but nothing on the far side of that
// seam is contractually bound to stay fast, and a stall there would freeze
// rendering and every key, ctrl+c included.
type threadToolCallCancelResultMsg struct {
	label string
	ok    bool
	err   error

	// on is the statusline the notice belongs on; nil means the foreground.
	// See subagentTaskCancelResultMsg.on for why a remote cancel must carry
	// its target's statusline rather than borrow whichever is on screen.
	on *statusline.Model
}

// handleThreadToolCallCancelResult emits the statusline notice for one
// finished tool-call cancel attempt: the error text on failure,
// "cancelling <label>" on success, and nothing on a miss (the call
// already finished, or the task registered no canceler).
func (s Screen) handleThreadToolCallCancelResult(msg threadToolCallCancelResultMsg) (app.Screen, tea.Cmd) {
	if msg.err != nil {
		s.noticeOn(msg.on, "cancel tool call failed: "+msg.err.Error())
		return s, nil
	}
	if msg.ok {
		s.noticeOn(msg.on, "cancelling "+msg.label)
	}
	return s, nil
}
