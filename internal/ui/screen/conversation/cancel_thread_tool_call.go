package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
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
	ok, err := s.threads.CancelSubagentToolCall(s.threadID, block.CallID)
	if err != nil {
		s.statusline.Notice("cancel tool call failed: " + err.Error())
		return s, nil
	}
	if ok {
		s.statusline.Notice("cancelling " + block.Header.Label)
	}
	return s, nil
}
