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
type threadEventMsg struct{ event uievent.Event }

// threadEndedMsg is the thread counterpart of turnEndedMsg.
type threadEndedMsg struct{}

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
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return threadEndedMsg{}
		}
		return threadEventMsg{event: ev}
	}
}

// LoadHistory replays a thread's prior turns into the transcript as
// finished blocks - no streaming, no side effects - so reopening a
// thread shows the conversation so far.
func (s *Screen) LoadHistory(msgs []ports.Message) {
	for _, m := range msgs {
		var ev uievent.Event
		switch m.Role {
		case "user":
			ev = uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: m.Text}}
		default:
			ev = uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: m.Text}}
		}
		next, _ := s.transcript.HandleEvent(ev)
		s.transcript = next
	}
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
// it does not exist yet. It reports whether a live thread surfaced;
// false means the caller falls back to the step-log view.
func (s *Screen) openThread(callID string) bool {
	if s.threads == nil {
		return false
	}
	if s.thread != nil && s.threadID == callID {
		return true
	}
	conv, ok := s.threads.Thread(callID)
	if !ok {
		return false
	}
	thread := NewThread(s.Theme, s.Tier, conv, render.DialogBodyWidth(contentWidth(s.width)), s.now)
	thread.themes = s.themes
	thread.SetCommands(s.composer.Commands())
	thread.SetCommandRunner(s.runner)
	thread.LoadHistory(conv.History())
	s.thread, s.threadID = &thread, callID
	return true
}

// closeThread drops the cached embedded screen when the panel closes:
// the thread Conversation keeps the authoritative history, so a later
// reopen rebuilds from it without losing anything.
func (s *Screen) closeThread() {
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
