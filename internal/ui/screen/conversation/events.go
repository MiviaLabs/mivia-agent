package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// turnEndedMsg signals the active TurnHandle's Events() channel closed.
// Defined alongside the read continuation (waitForEvent) so the type and
// its producer live together; the channel-close case in waitForEvent is
// the only sender.
type turnEndedMsg struct {
	sessionID string
}

// waitForEvent is the one real tea.Cmd that reads one event and, when
// the caller re-issues it after handling the Msg, reads the next one -
// the shape build spec section 4.5 requires: no goroutine touches the
// model, only this Cmd's own closure over the channel.
func waitForEvent(events <-chan uievent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return turnEndedMsg{}
		}
		return uievent.EventMsg{Event: ev, Source: events}
	}
}

// applyTheme adopts a new theme across every component this screen
// owns. Theme and Tier are plain value fields all the way down - there
// is no shared pointer - so a component this misses keeps rendering in
// the old theme until it is rebuilt.
//
// The cached subagent-thread Screen is included: openThread reuses it
// for the same call ID, so a thread opened before the switch and
// reopened after it would otherwise come back in the previous theme.
func (s Screen) applyTheme(msg app.ThemeChangedMsg) Screen {
	s.Theme, s.Tier = msg.Theme, msg.Tier
	s.transcript.SetTheme(msg.Theme, msg.Tier)
	s.composer.SetTheme(msg.Theme, msg.Tier)
	s.statusline.SetTheme(msg.Theme, msg.Tier)
	s.approval.Theme, s.approval.Tier = msg.Theme, msg.Tier
	s.history.Theme, s.history.Tier = msg.Theme, msg.Tier
	s.queueOverlay.Theme, s.queueOverlay.Tier = msg.Theme, msg.Tier
	s.blackboard.Theme, s.blackboard.Tier = msg.Theme, msg.Tier
	s.topbar.SetTheme(msg.Theme, msg.Tier)
	s.panel.list.Theme, s.panel.list.Tier = msg.Theme, msg.Tier
	s.welcome.SetTheme(msg.Theme, msg.Tier)
	if s.thread != nil {
		next := s.thread.applyTheme(msg)
		s.thread = &next
	}
	return s
}

func (s Screen) awaitSessionEvent(sessionID string, events <-chan uievent.Event) tea.Cmd {
	if s.embedded {
		return s.awaitEvent(events)
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return turnEndedMsg{sessionID: sessionID}
		}
		return uievent.EventMsg{SessionID: sessionID, Event: ev, Source: events}
	}
}

// send submits the composer's text to ports.Conversation and arms the turn.
// If a turn is currently active, the text is enqueued into s.queue and the
// composer is cleared; queued messages are automatically sent in order as each
// active turn completes. Empty text returns no-op. The provider error path
// appends to the transcript (where the user is looking) rather than failing silently.
func (s Screen) send() (app.Screen, tea.Cmd) {
	text := s.composer.SubmitText()
	// Trimmed, not just empty: the shape gate the history is validated
	// against rejects a user message whose content trims to nothing, so a
	// composer holding only spaces or a stray newline must be treated as
	// nothing to send. The composer is cleared so pressing Enter on blank
	// input looks like what it is rather than silently doing nothing.
	if strings.TrimSpace(text) == "" {
		s.composer.Clear()
		return s, nil
	}
	if s.compaction != nil {
		s.queue = append(s.queue, text)
		if s.queueOverlay.Active() {
			s.queueOverlay.SetItems(s.queue)
		}
		s.composer.Clear()
		s.statusline.Notice(fmt.Sprintf("message queued until compaction finishes (%d in queue)", len(s.queue)))
		return s, nil
	}
	if s.active != nil {
		s.queue = append(s.queue, text)
		if s.queueOverlay.Active() {
			s.queueOverlay.SetItems(s.queue)
		}
		s.composer.Clear()
		s.statusline.Notice(fmt.Sprintf("message queued (%d in queue)", len(s.queue)))
		return s, nil
	}
	return s.sendText(text)
}

func (s Screen) sendText(text string) (app.Screen, tea.Cmd) {
	return s.sendTextWithPersisted(text, "")
}

// sendTextWithPersisted submits text as the provider-facing turn while
// persisted (when non-empty) replaces it in conversation history - see
// intent.Send.PersistedText. An empty persisted keeps sendText's existing
// behavior: the sent text is what gets persisted too.
func (s Screen) sendTextWithPersisted(text, persisted string) (app.Screen, tea.Cmd) {
	s.history.Push(text)
	s.history.Close()
	handle, err := s.conv.Send(context.Background(), intent.Send{Text: text, PersistedText: persisted})
	if err != nil {
		var cmd tea.Cmd
		s.transcript, cmd = s.transcript.HandleEvent(uievent.Event{
			Kind: uievent.KindError,
			Body: uievent.ErrorBody{Text: err.Error(), Fatal: false},
		})
		return s, cmd
	}
	s.composer.Clear()
	s.active = handle
	s.refreshTopbar()
	cmd := s.statusline.Start("thinking", s.now())
	return s, tea.Batch(cmd, s.awaitSessionEvent(s.convID(), handle.Events()))
}

// handleTurnEvent routes one streamed uievent.Event into the transcript,
// the approval prompt, the status line, the files panel, and the next
// read continuation. The transcript render is the single source of truth
// for block shape; the panel and approval are side-effects of the same
// stream, fed here because this screen sees every event.
func (s Screen) handleTurnEvent(ev uievent.Event) (app.Screen, tea.Cmd) {
	next, flushCmd := s.transcript.HandleEvent(ev)
	s.transcript = next

	switch b := ev.Body.(type) {

	case uievent.ToolPendingBody:
		s.approval.SetRequest(b)
		s.statusline.SetLabel("pending")
		s.statusline.SetDetail(toolDetail(b.Name, b.Args))
		// The approval prompt claims the chat column and every key; a
		// content dialog open over that column would hide the prompt it
		// pre-empts, so it closes.
		s.panel.dialog, s.panel.dialogAgent = false, ""
	case uievent.ToolStartBody:
		// This call's OWN prompt, not every prompt. A blanket clear dismissed
		// the prompt for a different call that was still waiting, and its gate
		// then blocked with nothing on screen to answer it.
		s.approval.Resolve(b.ToolCallID)
		s.statusline.SetLabel("running")
		s.statusline.SetDetail(toolDetail(b.Name, b.Args))
		s.observeToolStart(b)
		s.recordBlackboardTool(b.Name, b.Args)
	case uievent.ToolOutputBody:
		// A progress-bearing output is a subagent status update (see
		// uievent.ToolOutputBody): the panel's subagents section feeds
		// from the same stream the transcript renders.
		if b.Progress != nil {
			s.panel.observeAgent(b.ToolCallID, b.Progress)
			if !s.statusline.Active() && s.panel.activeAgentCount() > 0 {
				tickCmd := statusline.TickCmd()
				flushCmd = tea.Batch(flushCmd, tickCmd)
			}
		}
	case uievent.ToolEndBody:
		s.approval.Resolve(b.ToolCallID)
		s.statusline.SetLabel("thinking")
		s.observeToolEnd(b)
	case uievent.UsageBody:
		// InputTokens is the whole prepared history the provider just
		// priced, so this is the context FILL LEVEL, not an increment.
		// OutputTokens is deliberately not folded into the gauge: the
		// reply is not part of the prompt that produced it, and the next
		// request's own InputTokens already counts it once it becomes
		// history. Adding it here would count it twice.
		live := ports.Usage{
			InputTokens:  b.InputTokens,
			CachedTokens: b.CachedTokens,
			CostUSD:      b.CostUSD,
		}
		s.liveUsage = &live
		s.topbar.SetUsage(live)
		s.statusline.SetCost(b.CostUSD)
	case uievent.TurnEndBody:
		// The turn is over, so no decision can reach a gate any more.
		s.approval.ClearAll()
		s.panel.reconcileTerminal(b.Reason)
		// The turn committed (and may have compacted at the boundary), so
		// the session's own estimate is authoritative again. Dropping the
		// live reading here is what lets a boundary compaction show up as
		// the gauge falling instead of staying pinned at the pre-compaction
		// high-water mark for the rest of the session.
		s.liveUsage = nil
	}

	s.refreshTopbar()

	var readCmd tea.Cmd

	if s.active != nil {
		readCmd = s.awaitSessionEvent(s.convID(), s.active.Events())
	}
	return s, tea.Batch(flushCmd, readCmd)
}

// observeToolStart folds one ToolStartBody into the activity panel. A
// dispatch_tasks call fires ONE tool.start for the whole batch, so it fans
// out into one row per dispatched task instead of a single aggregate row -
// otherwise the panel and the top-bar agent count would only ever show the
// call, not the subagents it dispatched.
func (s *Screen) observeToolStart(b uievent.ToolStartBody) {
	if !isSubagentTool(b.Name) && !(s.threads != nil && isThreadRegistered(s.threads, b.ToolCallID)) {
		return
	}
	if s.panel.isDispatchGroup(b.ToolCallID) {
		// The agent loop emits two tool.start events per call - "queued"
		// (internal/agent/sdk_tool_events.go, Args populated) then
		// "running" (sdk_dispatcher_shim.go's dispatcherShim.Run, which
		// never sets Input at all). The first one already fanned this
		// call out into its own per-task rows; a second event for the
		// SAME call id must not re-derive ids from a possibly-empty Args
		// and fall back to a stray single row keyed by the raw call id.
		return
	}
	if ids, names := dispatchTaskIDsAndNames(b.ToolCallID, b.Name, b.Args); len(ids) > 0 {
		s.panel.observeAgentGroupStart(b.ToolCallID, ids, names)
	} else {
		name := extractAgentDisplayName(b.Name, b.Args)
		s.panel.observeAgentStart(b.ToolCallID, name)
	}
}

func extractAgentDisplayName(toolName string, args map[string]any) string {
	for _, key := range []string{"agent", "subagent", "role", "type", "skill", "workflow", "name"} {
		if v := getStringVal(args, key); v != "" {
			return v
		}
	}
	return toolName
}

// observeToolEnd folds one ToolEndBody into the activity panel and the
// touched-files list: a tracked dispatch_tasks group resolves each
// dispatched task's own terminal status from the call's JSON result,
// instead of collapsing every task to the same aggregate ok/failed value.
func (s *Screen) observeToolEnd(b uievent.ToolEndBody) {
	if s.panel.isDispatchGroup(b.ToolCallID) {
		s.panel.observeAgentGroupEnd(b.ToolCallID, parseDispatchTaskStatuses(b.Result), b.OK)
	} else {
		s.panel.observeAgentEnd(b.ToolCallID, b.OK)
	}
	if b.Diff != nil {
		// The panel's data, fed live: every completed edit appears as a
		// touched file the moment it happens, exactly as the transcript
		// renders it. Deletions carry no diff in the event vocabulary, so
		// only edits and creations record here.
		s.panel.appendLive(*b.Diff)
	}
}

func isSubagentTool(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "agent_") ||
		strings.HasPrefix(lower, "subagent") ||
		strings.HasPrefix(lower, "delegate") ||
		strings.HasPrefix(lower, "invoke_") ||
		strings.HasPrefix(lower, "workflow_") ||
		strings.HasPrefix(lower, "dispatch_") ||
		strings.HasPrefix(lower, "spawn_") ||
		strings.HasPrefix(lower, "send_to_task") ||
		strings.Contains(lower, "orchestrat") ||
		strings.Contains(lower, "planner") ||
		strings.Contains(lower, "builder") ||
		strings.Contains(lower, "reviewer") ||
		strings.Contains(lower, "research")
}

func isThreadRegistered(threads ports.SubagentThreads, callID string) bool {
	if threads == nil {
		return false
	}
	_, ok := threads.Thread(callID)
	return ok
}

// dispatchTaskIDs extracts per-task ids from a dispatch_tasks call's own
// arguments, mirroring thread.go's LoadHistory reconstruction of a
// resumed session's dispatch_tasks entries - so a LIVE dispatch_tasks run
// fans out into one panel row per dispatched task, keyed the same way,
// instead of one aggregate row for the whole call. Returns nil for every
// other tool name, or when no task list can be parsed, leaving the
// caller's existing single-row behavior unchanged.
func dispatchTaskIDs(callID, name string, args map[string]any) []string {
	ids, _ := dispatchTaskIDsAndNames(callID, name, args)
	return ids
}

// dispatchTaskIDsAndNames extracts both per-task ids and friendly agent names.
func dispatchTaskIDsAndNames(callID, name string, args map[string]any) ([]string, map[string]string) {
	if strings.ToLower(name) != "dispatch_tasks" {
		return nil, nil
	}
	rawTasks, ok := args["tasks"].([]any)
	if !ok || len(rawTasks) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rawTasks))
	names := make(map[string]string, len(rawTasks))
	for i, rt := range rawTasks {
		id := ""
		taskName := ""
		if m, ok := rt.(map[string]any); ok {
			if s, ok := m["id"].(string); ok {
				id = s
			}
			taskName = extractAgentDisplayName("", m)
		}
		if id == "" {
			id = fmt.Sprintf("task-%d", i+1)
		} else {
			id = namespacedTaskID(callID, id)
		}
		ids = append(ids, id)
		if taskName != "" {
			names[id] = taskName
		}
	}
	return ids, names
}

// namespacedTaskID mirrors internal/cliorchestrate's function of the same
// name. Duplicated, not imported: internal/ui/** must not import
// internal/cli*-family packages (UI isolation, docs/design/ui-isolation.md,
// enforced by scripts/check_import_layers.py), so the two copies are kept
// in sync by contract, not by the compiler.
func namespacedTaskID(namespace, rawID string) string {
	if namespace == "" || rawID == "" {
		return rawID
	}
	return namespace + ":" + rawID
}

// parseDispatchTaskStatuses decodes a dispatch_tasks call's own JSON result
// into a task id -> status map, accepting either the bare-array shape
// (wait="run") or the wrapped {"tasks":[...]} / {"task_results":[...]} envelopes (wait="none"/
// "task") - render/output_formatter.go and uiadapter/subagent_reconstruct.go
// handle the same duality for the transcript and the resumed-session
// reconstruction paths respectively. Returns nil when result matches
// neither shape, so the caller falls back to the aggregate ok flag.
func parseDispatchTaskStatuses(result string) map[string]string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return nil
	}
	type row struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	var rows []row
	var wrapped struct {
		TaskResults []row `json:"task_results"`
		Tasks       []row `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil || len(rows) == 0 {
		if err := json.Unmarshal([]byte(trimmed), &wrapped); err != nil {
			return nil
		}
	}
	out := make(map[string]string, len(wrapped.Tasks)+len(wrapped.TaskResults)+len(rows))
	for _, r := range wrapped.Tasks {
		id := r.TaskID
		if id == "" {
			id = r.ID
		}
		if id != "" && r.Status != "" {
			out[id] = r.Status
		}
	}
	for _, r := range wrapped.TaskResults {
		id := r.TaskID
		if id == "" {
			id = r.ID
		}
		if id != "" && r.Status != "" {
			out[id] = r.Status
		}
	}
	for _, r := range rows {
		id := r.TaskID
		if id == "" {
			id = r.ID
		}
		if id != "" && r.Status != "" {
			out[id] = r.Status
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Screen) recordBlackboardTool(name string, args map[string]any) {
	if len(args) == 0 {
		return
	}
	lower := strings.ToLower(name)
	switch lower {
	case "post_message":
		kind := strings.ToLower(getStringVal(args, "kind"))
		body := getStringVal(args, "body")
		if kind == "finding" {
			var refs []string
			if rList, ok := args["refs"].([]any); ok {
				for _, r := range rList {
					refs = append(refs, fmt.Sprint(r))
				}
			}
			s.blackboard.AddFinding("subagent", body, refs)
		} else if kind != "" && body != "" {
			toRole := getStringVal(args, "to_role")
			if toRole == "" {
				toRole = "orchestrator"
			}
			s.blackboard.AddMessage("subagent", toRole, kind, body)
		}
	case "send_to_task":
		action := getStringVal(args, "action")
		taskID := getStringVal(args, "task_id")
		body := getStringVal(args, "body")
		if action == "" {
			action = "steer"
		}
		if body != "" {
			s.blackboard.AddMessage("orchestrator", taskID, action, body)
		}
	case "send_message":
		recipient := getStringVal(args, "Recipient")
		if recipient == "" {
			recipient = getStringVal(args, "recipient")
		}
		msg := getStringVal(args, "Message")
		if msg == "" {
			msg = getStringVal(args, "message")
		}
		if msg != "" {
			s.blackboard.AddMessage("orchestrator", recipient, "message", msg)
		}
	}
}

func getStringVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
		return fmt.Sprint(v)
	}
	return ""
}
