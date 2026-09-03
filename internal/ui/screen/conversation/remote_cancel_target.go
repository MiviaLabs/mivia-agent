package conversation

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// remoteCancelTarget is the parsed Body of a targeted remote cancel
// ("cancel_task" / "cancel_tool_call"). The wire contract reuses the
// existing Body field to carry ids rather than adding a wire field:
//
//	cancel_task      "<subagent row id>"
//	cancel_tool_call "<tool call id>"              MAIN turn
//	cancel_tool_call "<row id> <tool call id>"     inside that subagent
//
// The subagent row id is the namespaced "<dispatching tool call id>:<task
// id>" that internal/cliorchestrate's registerSubagentTaskRoutes registers
// and events.go's dispatchTaskIDsAndNames builds panel rows with - the same
// string the local files-panel cancel key passes.
type remoteCancelTarget struct {
	// id is the first part: a subagent row id for "cancel_task" and for a
	// two-part "cancel_tool_call", or a MAIN-turn tool call id for a
	// one-part "cancel_tool_call".
	id string
	// toolCallID is the second part, empty when the body carried only one.
	toolCallID string
}

// parseRemoteCancelTarget splits a targeted cancel body into its one or two
// non-empty parts. Surrounding and repeated whitespace is tolerated
// (strings.Fields), because a body that only differs by padding names the
// same target. Anything else - empty, whitespace only, or three or more
// parts - is a refusal (ok false): guessing which parts were meant would
// route a cancel at something the sender did not name.
func parseRemoteCancelTarget(body string) (remoteCancelTarget, bool) {
	parts := strings.Fields(body)
	if len(parts) == 1 {
		return remoteCancelTarget{id: parts[0]}, true
	}
	if len(parts) == 2 {
		return remoteCancelTarget{id: parts[0], toolCallID: parts[1]}, true
	}
	return remoteCancelTarget{}, false
}

// remoteCancelSeams are the one target session's cancel surfaces: its active
// turn handle (for a MAIN-turn tool call), its subagent route seam (for
// anything inside a dispatched task), and its own statusline, so a notice
// lands on the session the instruction targeted rather than on whichever
// session happens to be on screen.
type remoteCancelSeams struct {
	active     ports.TurnHandle
	threads    ports.SubagentThreads
	statusline *statusline.Model
}

// resolveRemoteCancelSeams resolves the session a remote instruction
// targets, using EXACTLY the fork handleRemoteCancel uses: an empty or
// matching session id means the foreground conversation, anything else must
// be a background session this screen already tracks in s.sessions. ok is
// false for a session that is neither - there is nothing here to cancel
// through.
//
// Pointer receiver on purpose: the returned *statusline.Model must point at
// the SAME Screen copy the calling value-receiver method returns, so a
// notice written through it survives.
func (s *Screen) resolveRemoteCancelSeams(sessionID string) (remoteCancelSeams, bool) {
	if sessionID == "" || sessionID == s.convID() {
		return remoteCancelSeams{active: s.active, threads: s.threads, statusline: &s.statusline}, true
	}
	st, ok := s.sessions[sessionID]
	if !ok {
		return remoteCancelSeams{}, false
	}
	return remoteCancelSeams{active: st.active, threads: st.threads, statusline: &st.statusline}, true
}

// handleRemoteTargetedCancel routes one "cancel_task" or "cancel_tool_call"
// remote instruction. Unlike the untargeted "cancel", these name an id, so a
// stale one delivered after its target finished resolves to a harmless miss
// instead of stopping unrelated work.
//
// Every miss - unparseable body, untracked session, no seam wired, an id
// that names nothing live - is a status notice or a silent no-op. None of
// them is an error and none kills a turn.
func (s Screen) handleRemoteTargetedCancel(ev ports.RemoteInputEvent) (app.Screen, tea.Cmd) {
	target, ok := parseRemoteCancelTarget(ev.Body)
	if !ok {
		s.statusline.Notice("remote " + ev.Kind + " ignored: malformed target")
		return s, nil
	}
	if ev.Kind == "cancel_task" {
		return s.remoteCancelSubagentTask(ev, target)
	}
	return s.remoteCancelToolCall(ev, target)
}

// remoteCancelSubagentTask cancels the ONE dispatched subagent task named by
// a "cancel_task" body, through the same ports.SubagentThreads seam the
// local files-panel key uses (cancel_subagent_task.go).
//
// The seam call runs inside the returned tea.Cmd, NEVER inline:
// CancelSubagentTask reaches the coordinator's per-task cancel, which blocks
// for its whole wait budget (seconds) while the task unwinds. Inline, that
// froze rendering and every message on bubbletea's single Update goroutine -
// the freeze fixed in e19cd048 for the local key path. The remote path must
// not reintroduce it.
func (s Screen) remoteCancelSubagentTask(ev ports.RemoteInputEvent, target remoteCancelTarget) (app.Screen, tea.Cmd) {
	seams, ok := s.resolveRemoteCancelSeams(ev.SessionID)
	if !ok {
		return s, nil
	}
	if target.toolCallID != "" {
		seams.statusline.Notice("remote cancel_task ignored: body must name exactly one task")
		return s, nil
	}
	if seams.threads == nil {
		return s, nil
	}
	threads, rowID, on := seams.threads, target.id, seams.statusline
	return s, func() tea.Msg {
		ok, err := threads.CancelSubagentTask(rowID)
		return subagentTaskCancelResultMsg{name: rowID, ok: ok, err: err, on: on}
	}
}

// remoteCancelToolCall cancels the ONE in-flight tool call a
// "cancel_tool_call" body names. A one-part body targets the MAIN turn and
// goes through the turn handle's own CancelToolCall (the same call
// cancel_tool_call.go's local key makes, which is in-process and does not
// block). A two-part body targets a call INSIDE a dispatched subagent and
// goes through ports.SubagentThreads.CancelSubagentToolCall - argument order
// is (row id, tool call id), matching cancel_thread_tool_call.go.
//
// The subagent leg runs in a tea.Cmd for the same reason
// remoteCancelSubagentTask's does: nothing past that seam is contractually
// bound to stay fast, and a stall there would freeze the Update goroutine.
func (s Screen) remoteCancelToolCall(ev ports.RemoteInputEvent, target remoteCancelTarget) (app.Screen, tea.Cmd) {
	seams, ok := s.resolveRemoteCancelSeams(ev.SessionID)
	if !ok {
		return s, nil
	}
	if target.toolCallID == "" {
		if seams.active == nil {
			return s, nil
		}
		if seams.active.CancelToolCall(target.id) {
			seams.statusline.Notice("cancelling " + target.id)
		}
		return s, nil
	}
	if seams.threads == nil {
		return s, nil
	}
	threads, rowID, toolCallID, on := seams.threads, target.id, target.toolCallID, seams.statusline
	return s, func() tea.Msg {
		ok, err := threads.CancelSubagentToolCall(rowID, toolCallID)
		return threadToolCallCancelResultMsg{label: toolCallID, ok: ok, err: err, on: on}
	}
}
