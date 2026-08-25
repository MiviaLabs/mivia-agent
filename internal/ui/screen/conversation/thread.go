// thread is the subagent-thread half of the activity panel: a
// conversation.Screen rendered INSIDE the content dialog, in a
// minimal-chrome mode. The requirement is centralisation, not
// resemblance: this is the SAME Screen type, the SAME composer.Model,
// the SAME transcript.Model, and the SAME Send -> TurnHandle ->
// event-loop path the main chat runs - instantiated a second time
// against the subagent's own ports.Conversation and asked to draw
// without its top bar. A change to any of that machinery shows up in
// both surfaces; there is no second implementation to drift.
//
// Event plumbing: the embedded screen's read continuations wrap their
// Msgs (threadEventMsg, threadEndedMsg) so the MAIN screen - the only
// Screen the router delivers to - can tell a subagent thread's events
// from its own turn's and forward them. Ticks and flushes are safe to
// hand to both screens: they are idempotent repaint drivers.
package conversation

import (
	"encoding/json"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// threadEventMsg is one streamed event from the ACTIVE subagent
// thread's turn. It exists because uievent.EventMsg is also the main
// chat's turn vocabulary: without an origin marker the main screen
// could not tell whose event it is, and the thread's reply would render
// into the main transcript.
type threadEventMsg struct {
	callID string
	event  uievent.Event
}

// threadEndedMsg is the thread counterpart of turnEndedMsg.
type threadEndedMsg struct {
	callID string
}

// NewThread builds the embedded conversation screen for one subagent
// thread. It is New plus the embedded flag: the same construction, the
// same components, sized by the caller to the dialog's body area.
func NewThread(t theme.Theme, tier theme.Tier, conv ports.Conversation, width int, now func() time.Time) Screen {
	s := New(t, tier, nil, conv, nil, width, now)
	s.embedded = true
	return s
}

// awaitEvent is the one read continuation factory. The main screen
// yields the bare Msgs (waitForEvent); an embedded screen wraps them so
// its events bubble up MARKED and the main screen forwards them here
// instead of consuming them as its own turn.
func (s Screen) awaitEvent(events <-chan uievent.Event) tea.Cmd {
	if !s.embedded {
		return waitForEvent(events)
	}
	callID := s.threadID
	if callID == "" && s.conv != nil {
		callID = s.conv.ID()
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return threadEndedMsg{callID: callID}
		}
		return threadEventMsg{callID: callID, event: ev}
	}
}

func parseToolArgs(args string) map[string]any {
	if args == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(args), &out); err == nil {
		return out
	}
	return nil
}

// LoadHistory replays a thread's prior turns into the transcript as
// finished blocks - no streaming, no side effects - so reopening a
// thread shows the conversation so far.
func (s *Screen) LoadHistory(msgs []ports.Message) {
	for i, m := range msgs {
		isLastMsg := (i == len(msgs)-1)
		switch m.Role {
		case "user":
			s.history.Push(m.Text)
			ev := uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: m.Text}}
			s.transcript, _ = s.transcript.HandleEvent(ev)
		default:
			if m.Reasoning != "" {
				s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
					Kind: uievent.KindReasoning,
					Body: uievent.ReasoningDeltaBody{Text: m.Reasoning, WordCount: len(strings.Fields(m.Reasoning))},
				})
			}
			for _, tc := range m.ToolCalls {
				s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
					Kind: uievent.KindToolStart,
					Body: uievent.ToolStartBody{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Args:       parseToolArgs(tc.Arguments),
					},
				})
				s.transcript, _ = s.transcript.HandleEvent(uievent.Event{
					Kind: uievent.KindToolEnd,
					Body: uievent.ToolEndBody{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						OK:         true,
						Result:     tc.Output,
					},
				})
				if isSubagentTool(tc.Name) || (s.threads != nil && isThreadRegistered(s.threads, tc.ID)) {
					status := "completed"
					if tc.Output == "" && isLastMsg && m.Text == "" {
						status = "interrupted"
					}
					s.panel.observeAgentHistory(tc.ID, status)
				}
			}
			if m.Text != "" {
				ev := uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: m.Text}}
				s.transcript, _ = s.transcript.HandleEvent(ev)
			}
		}
		for _, d := range m.Diffs {
			s.panel.appendLive(d)
		}
	}
	s.refreshTopbar()
}

// setSurface is the embedded screen's resize entry point: the dialog
// owns the geometry, so the thread is told its area rather than
// listening for WindowSizeMsg (which belongs to the real terminal).
func (s *Screen) setSurface(width, height int) {
	s.width, s.height = width, height
	s.reflow()
}

// openThread selects callID's thread: reusing the cached Screen when
// the dialog reopens on the same subagent (its transcript IS the
// ongoing state), building a fresh one from the thread's History when
// it does not exist yet. It reports whether a live thread surfaced and
// any initial stream command scheduled.
func (s *Screen) openThread(callID string) (bool, tea.Cmd) {
	if s.threads == nil {
		return false, nil
	}
	if s.thread != nil && s.threadID == callID {
		return true, nil
	}
	if s.thread != nil && s.thread.active != nil {
		s.thread.active.Cancel()
	}
	conv, ok := s.threads.Thread(callID)
	if !ok {
		return false, nil
	}
	thread := NewThread(s.Theme, s.Tier, conv, render.DialogBodyWidth(contentWidth(s.width)), s.now)
	thread.themes = s.themes
	thread.threadID = callID
	thread.SetCommands(s.composer.Commands())
	thread.SetCommandRunner(s.runner)
	thread.LoadHistory(conv.History())
	s.thread, s.threadID = &thread, callID

	var cmd tea.Cmd
	if handle, running := conv.ActiveTurn(); running && handle != nil {
		s.thread.active = handle
		cmd = s.thread.awaitEvent(handle.Events())
	}
	return true, cmd
}

// closeThread drops the cached embedded screen when the panel closes:
// the thread Conversation keeps the authoritative history, so a later
// reopen rebuilds from it without losing anything.
func (s *Screen) closeThread() {
	if s.thread != nil && s.thread.active != nil {
		s.thread.active.Cancel()
	}
	s.thread, s.threadID = nil, ""
}

// threadDialogKey routes a key into the open subagent-thread dialog.
// Esc, ctrl+b, and ctrl+c close the dialog back to the list (the thread
// keeps streaming in the background and reopens with its state).
// Everything else goes to the embedded screen's OWN Update - its composer,
// its completion menu, its transcript - never the main chat's.
func (s Screen) threadDialogKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+b", "ctrl+c":
		s.panel.dialog, s.panel.dialogAgent = false, ""
		return s, tea.ClearScreen
	case "pgup":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(-max(1, s.thread.transcriptHeight()/2))
			return s, nil
		}
	case "pgdown":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(max(1, s.thread.transcriptHeight()/2))
			return s, nil
		}
	case "home", "ctrl+home":
		if s.thread != nil && s.thread.composer.Value() == "" {
			s.thread.transcript = s.thread.transcript.ScrollToTop()
			return s, nil
		}
	case "end", "ctrl+end":
		if s.thread != nil && s.thread.composer.Value() == "" {
			s.thread.transcript = s.thread.transcript.ScrollToBottom()
			return s, nil
		}
	case "ctrl+u":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(-max(1, s.thread.transcriptHeight()/2))
			return s, nil
		}
	case "ctrl+d":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(max(1, s.thread.transcriptHeight()/2))
			return s, nil
		}
	case "up":
		if s.thread != nil && s.thread.composer.Value() == "" {
			s.thread.transcript = s.thread.transcript.ScrollBy(-1)
			return s, nil
		}
	case "down":
		if s.thread != nil && s.thread.composer.Value() == "" {
			s.thread.transcript = s.thread.transcript.ScrollBy(1)
			return s, nil
		}
	}
	if s.thread == nil {
		return s, nil
	}
	next, cmd := s.thread.Update(msg)
	// Update returns the model BY VALUE (the tea convention); the cached
	// pointer must point at the fresh copy or every routed key mutates a
	// discarded value.
	if t, ok := next.(Screen); ok {
		s.thread = &t
	}
	return s, cmd
}

// forwardThreadMsg hands one of the wrapped thread Msgs to the cached
// embedded screen and returns whatever Cmd it schedules (its next read
// continuation, flushes). With no thread cached it is a no-op.
func (s Screen) forwardThreadMsg(msg tea.Msg) (app.Screen, tea.Cmd) {
	if s.thread == nil {
		return s, nil
	}
	switch m := msg.(type) {
	case threadEventMsg:
		if m.callID != "" && m.callID != s.threadID {
			return s, nil
		}
	case threadEndedMsg:
		if m.callID != "" && m.callID != s.threadID {
			return s, nil
		}
	}
	next, cmd := s.thread.Update(msg)
	// Update returns the model BY VALUE (the tea convention); the cached
	// pointer must point at the fresh copy or every routed key mutates a
	// discarded value.
	if t, ok := next.(Screen); ok {
		s.thread = &t
	}
	return s, cmd
}

// forwardSharedMsg drives the embedded screen with a Msg that is safe
// to hand to both surfaces (statusline ticks, transcript flushes):
// they are idempotent repaint clocks, so double delivery cannot double
// anything a user sees.
func (s Screen) forwardSharedMsg(msg tea.Msg) {
	if s.thread == nil {
		return
	}
	next, _ := s.thread.Update(msg)
	if t, ok := next.(Screen); ok {
		s.thread = &t
	}
}
