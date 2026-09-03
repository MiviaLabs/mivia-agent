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
	"fmt"
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
					if strings.ToLower(tc.Name) == "dispatch_tasks" {
						var args struct {
							Tasks []struct {
								ID string `json:"id"`
							} `json:"tasks"`
						}
						if json.Unmarshal([]byte(tc.Arguments), &args) == nil && len(args.Tasks) > 0 {
							for i, t := range args.Tasks {
								tid := t.ID
								if tid == "" {
									// Must match dispatchTaskIDs' fallback in
									// events.go: never embed the raw
									// provider tool_call_id (tc.ID) in a
									// visible row id.
									tid = fmt.Sprintf("task-%d", i+1)
								} else {
									// Must match dispatchTaskIDs/dispatchNamespace:
									// the real per-task id a live dispatch minted
									// was tc.ID+":"+t.ID, not t.ID verbatim.
									tid = namespacedTaskID(tc.ID, tid)
								}
								s.panel.observeAgentHistory(tid, status)
							}
						} else {
							s.panel.observeAgentHistory(tc.ID, status)
						}
					} else {
						s.panel.observeAgentHistory(tc.ID, status)
					}
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
//
// The composer is always hidden: a subagent thread dialog is a
// read-only view onto another agent's conversation for the operator,
// running or finished. SubagentTranscriptConversation.Send (see
// uiadapter/subagent.go) has no route to the real running subagent
// either - text typed there would only ever land in a dead-end
// transcript - so gating visibility on terminal status (as an earlier
// version did) offered interactivity that did nothing.
func (s *Screen) openThread(callID string) (bool, tea.Cmd) {
	if s.threads == nil {
		return false, nil
	}
	if s.thread != nil && s.threadID == callID {
		s.thread.SetHideComposer(true)
		return true, nil
	}
	// DETACH, not abort: this drops the previous thread's UI listener. It
	// relies on the subagent transcript handles' divergent Cancel (see
	// ports.TurnHandle.Cancel). conv here is whatever SubagentThreads.Thread
	// returned, and a FOREIGN ports.Conversation implementing Cancel to the
	// port's letter would have a real turn aborted here. Only register
	// detach-implementing conversations with SubagentThreads.
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
	thread.SetHideComposer(true)
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
	// DETACH, not abort - same divergence and same foreign-conversation
	// hazard as openThread's call above; see ports.TurnHandle.Cancel.
	if s.thread != nil && s.thread.active != nil {
		s.thread.active.Cancel()
	}
	s.thread, s.threadID = nil, ""
}

// threadDialogKey routes a key into the open subagent-thread dialog.
// Esc, ctrl+b, and ctrl+c close the dialog back to the list (the thread
// keeps streaming in the background and reopens with its state).
// Arrows over a visible, non-empty composer are caret keys and stop at
// that composer - they must never reach the prompt-history overlay.
// Everything else goes to the embedded screen's OWN Update - its composer,
// its completion menu, its transcript - never the main chat's.
func (s Screen) threadDialogKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if msg.String() == "esc" || msg.String() == "ctrl+b" || msg.String() == "ctrl+c" {
		s.panel.dialog, s.panel.dialogAgent = false, ""
		return s, tea.ClearScreen
	}
	if next, cmd, handled := s.threadDialogScrollKey(msg); handled {
		return next, cmd
	}
	if s.thread == nil {
		return s, nil
	}
	if s.thread.hideComposer {
		return s, nil
	}
	if next, cmd, handled := s.routeThreadDialogArrows(msg); handled {
		return next, cmd
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

// threadDialogScrollKey handles every named key threadDialogKey answers
// itself rather than forwarding to the embedded screen's Update: scrolling
// (pgup/pgdown/home/end/ctrl+u/ctrl+d/up/down/j/k, gated the same way the
// original single switch was) plus tab/shift+tab/x, which mirror the main
// transcript's own ContextTranscript bindings
// (keymap.IDFocusNext/IDFocusPrev/IDCancelToolCall) at THIS dialog's own
// transcript: the composer is always hidden here (see openThread's doc
// comment), so there is no composer-side shift+tab to enter focus mode
// from - these three cases are that entry point, plus navigation, plus the
// cancel action, scoped to s.thread.transcript rather than the outer
// s.transcript. See cancel_thread_tool_call.go's doc comment for why
// reusing the same keys here is unambiguous. Split out of threadDialogKey
// to keep it under the per-function line budget; reports handled=false for
// any key not in this table so the caller falls through to its own
// composer-Update path.
func (s Screen) threadDialogScrollKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	composerReady := s.thread != nil && (s.thread.hideComposer || s.thread.composer.Value() == "")
	hidden := s.thread != nil && s.thread.hideComposer
	switch msg.String() {
	case "pgup":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(-max(1, s.thread.transcriptHeight()/2))
			return s, nil, true
		}
	case "pgdown":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(max(1, s.thread.transcriptHeight()/2))
			return s, nil, true
		}
	case "home", "ctrl+home":
		if composerReady {
			s.thread.transcript = s.thread.transcript.ScrollToTop()
			return s, nil, true
		}
	case "end", "ctrl+end":
		if composerReady {
			s.thread.transcript = s.thread.transcript.ScrollToBottom()
			return s, nil, true
		}
	case "ctrl+u":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(-max(1, s.thread.transcriptHeight()/2))
			return s, nil, true
		}
	case "ctrl+d":
		if s.thread != nil {
			s.thread.transcript = s.thread.transcript.ScrollBy(max(1, s.thread.transcriptHeight()/2))
			return s, nil, true
		}
	case "up":
		if composerReady {
			s.thread.transcript = s.thread.transcript.ScrollBy(-1)
			return s, nil, true
		}
	case "down":
		if composerReady {
			s.thread.transcript = s.thread.transcript.ScrollBy(1)
			return s, nil, true
		}
	// Typeable keys belong to any composer that can take input; j and k scroll only hidden-composer dialogs.
	case "k":
		if hidden {
			s.thread.transcript = s.thread.transcript.ScrollBy(-1)
			return s, nil, true
		}
	case "j":
		if hidden {
			s.thread.transcript = s.thread.transcript.ScrollBy(1)
			return s, nil, true
		}
	case "tab":
		if hidden {
			s.thread.transcript = s.thread.transcript.FocusNext()
			return s, nil, true
		}
	case "shift+tab":
		if hidden {
			s.thread.transcript = s.thread.transcript.FocusPrev()
			return s, nil, true
		}
	case "x":
		if hidden {
			next, cmd := s.cancelFocusedThreadToolCall()
			return next, cmd, true
		}
	}
	return s, nil, false
}

// routeThreadDialogArrows turns up and down over a visible, non-empty
// composer into caret keys. They go straight to the composer here, not
// through the embedded screen's key table: that table answers "up" on
// line 0 by opening the prompt-history overlay, and an overlay must never
// grow over a live modal dialog (its reserved rows also shift the dialog's
// transcript layout). An open completion or mention menu keeps the full
// route - those keys belong to menu navigation there. Reports whether the
// key was handled.
func (s Screen) routeThreadDialogArrows(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if key := msg.String(); key == "up" || key == "down" {
		if s.thread.composer.Value() != "" &&
			!s.thread.composer.MenuActive() &&
			!s.thread.composer.MentionMenuActive() {
			next, cmd := s.thread.composer.Update(msg)
			s.thread.composer = next
			return s, cmd, true
		}
	}
	return s, nil, false
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
