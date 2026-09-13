package conversation

import (
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// C7's screen half (docs/design/chat-tui-crush-comparison.md §3): the root
// transcript's subagent progress events carry counters and log text only, so
// the child calls a dispatched batch made are read from the one place this
// screen already owns - ports.SubagentThreads.Thread(callID).History() - and
// pushed into the dispatch row's compact child tree. The read happens on the
// event the screen is already handling; nothing here arms a clock (the
// one-spinner-clock rule, .agents/memories/tui-spinner-clock-*.md).

// syncThreadChildrenFor refreshes callID's child tree after a progress or
// tool-end event for toolCallID. A dispatch_tasks batch emits per-task ids
// ("call:task"), so the row id is first mapped back to the call that fanned
// it out; a thread registered under its own call id (the legacy single-agent
// tools openThread also resolves) maps to itself.
func (s *Screen) syncThreadChildrenFor(toolCallID string) {
	parent := parentDispatchCallID(&s.panel, toolCallID)
	if parent == "" {
		parent = toolCallID
	}
	s.syncThreadChildren(parent)
}

// syncThreadChildren collects the batch's settled child calls and hands them
// to the transcript. Threads that never registered (a task the registry has
// no conversation for) contribute nothing; with no thread at all the call is
// a quiet miss, never a cleared tree.
func (s *Screen) syncThreadChildren(callID string) {
	if s.threads == nil || callID == "" {
		return
	}
	children := collectChildCalls(s.threads, s.panel.dispatchGroups[callID], callID)
	if len(children) == 0 {
		return
	}
	s.transcript.SetChildren(callID, children)
}

// parentDispatchCallID maps a per-task panel row id back to the
// dispatch_tasks call whose tool.start fanned it out (filespanel's
// dispatchGroups is the parent -> row-ids map observeAgentGroupStart built).
func parentDispatchCallID(p *panel, taskRowID string) string {
	for parent, ids := range p.dispatchGroups {
		for _, id := range ids {
			if id == taskRowID {
				return parent
			}
		}
	}
	return ""
}

// collectChildCalls walks each task's thread history and flattens the tool
// calls into the compact ChildCall vocabulary. History records a successful
// empty-output call with OK=true, so that flag is part of the settled test;
// non-empty output and diffs cover older records that predate OK. A failed
// empty-output call remains indistinguishable from an in-flight call in old
// history and is conservatively omitted rather than rendered as a false "x".
func collectChildCalls(threads ports.SubagentThreads, taskIDs []string, callID string) []transcript.ChildCall {
	ids := taskIDs
	if len(ids) == 0 {
		ids = []string{callID}
	}
	var out []transcript.ChildCall
	for _, id := range ids {
		conv, ok := threads.Thread(id)
		if !ok || conv == nil {
			continue
		}
		for _, msg := range conv.History() {
			for _, tc := range msg.ToolCalls {
				if !childCallSettled(tc) {
					continue
				}
				out = append(out, transcript.ChildCall{
					Name:   tc.Name,
					Detail: render.FormatToolDetail(tc.Name, parseToolArgs(tc.Arguments)),
					OK:     tc.OK,
				})
			}
		}
	}
	return out
}

// childCallSettled reports a call whose end event has been folded into the
// recorded history, so its OK field is the real verdict.
func childCallSettled(tc ports.ToolCall) bool {
	return tc.OK || tc.Output != "" || tc.Diff != nil
}
