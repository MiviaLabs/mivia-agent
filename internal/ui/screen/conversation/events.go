package conversation

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
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
		return uievent.EventMsg{Event: ev}
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
		return uievent.EventMsg{SessionID: sessionID, Event: ev}
	}
}

// send submits the composer's text to ports.Conversation and arms the turn.
// If a turn is currently active, the text is enqueued into s.queue and the
// composer is cleared; queued messages are automatically sent in order as each
// active turn completes. Empty text returns no-op. The provider error path
// appends to the transcript (where the user is looking) rather than failing silently.
func (s Screen) send() (app.Screen, tea.Cmd) {
	text := s.composer.SubmitText()
	if text == "" {
		return s, nil
	}
	if s.active != nil {
		s.queue = append(s.queue, text)
		s.composer.Clear()
		s.statusline.Notice(fmt.Sprintf("message queued (%d in queue)", len(s.queue)))
		return s, nil
	}
	return s.sendText(text)
}

func (s Screen) sendText(text string) (app.Screen, tea.Cmd) {
	s.history.Push(text)
	s.history.Close()
	handle, err := s.conv.Send(context.Background(), intent.Send{Text: text})
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
		s.approval.Clear()
		s.statusline.SetLabel("running")
		s.statusline.SetDetail(toolDetail(b.Name, b.Args))
		if isSubagentTool(b.Name) || (s.threads != nil && isThreadRegistered(s.threads, b.ToolCallID)) {
			s.panel.observeAgentStart(b.ToolCallID, b.Name)
		}
	case uievent.ToolOutputBody:
		// A progress-bearing output is a subagent status update (see
		// uievent.ToolOutputBody): the panel's subagents section feeds
		// from the same stream the transcript renders.
		if b.Progress != nil {
			s.panel.observeAgent(b.ToolCallID, b.Progress)
		}
	case uievent.ToolEndBody:
		s.approval.Clear()
		s.statusline.SetLabel("thinking")
		s.panel.observeAgentEnd(b.ToolCallID, b.OK)
		if b.Diff != nil {
			// The panel's data, fed live: every completed edit appears
			// as a touched file the moment it happens, exactly as the
			// transcript renders it. Deletions carry no diff in the
			// event vocabulary, so only edits and creations record here.
			s.panel.appendLive(*b.Diff)
		}
	case uievent.UsageBody:
		s.topbar.SetUsage(ports.Usage{
			InputTokens:  b.InputTokens,
			OutputTokens: b.OutputTokens,
			CachedTokens: b.CachedTokens,
			CostUSD:      b.CostUSD,
		})
		s.statusline.SetCost(b.CostUSD)
	case uievent.TurnEndBody:
		s.approval.Clear()
		s.panel.reconcileTerminal(b.Reason)
	}

	s.refreshTopbar()

	var readCmd tea.Cmd

	if s.active != nil {
		readCmd = s.awaitSessionEvent(s.convID(), s.active.Events())
	}
	return s, tea.Batch(flushCmd, readCmd)
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
