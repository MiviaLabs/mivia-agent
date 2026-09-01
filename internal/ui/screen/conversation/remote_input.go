package conversation

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// remoteInputTagPrefix marks a remote-originated turn in the transcript.
// Applied ONLY through intent.Send.PersistedText, never Text: Text is what
// reaches the model, and injecting a tag there would change model input with
// attacker-adjacent text (the tag itself becomes part of the prompt an
// unverified-until-here string sits next to). PersistedText is display-only -
// see internal/uiadapter/conversation.go's Conversation.Send, which uses
// PersistedText for the transcript line and Text for the provider request.
const remoteInputTagPrefix = "(via web) "

// remoteInputMsg wraps one already-validated ports.RemoteInputEvent for
// bubbletea message delivery. Everything this screen needs to trust about
// the event was already checked in internal/chatsync before it ever reached
// the port - see ports.RemoteInputEvent's doc comment.
type remoteInputMsg struct {
	event ports.RemoteInputEvent
}

// awaitRemoteInput is the read continuation for the inbound steering port,
// the same one-value-then-rearm shape waitForEvent uses for turn events. A
// nil channel (SetRemoteInputs was never called - every embedded thread
// Screen, and any Screen a test builds without wiring one) returns a nil
// Cmd: no goroutine is armed, and no Msg is ever produced.
func (s Screen) awaitRemoteInput() tea.Cmd {
	if s.remoteInputs == nil {
		return nil
	}
	ch := s.remoteInputs
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return remoteInputMsg{event: ev}
	}
}

// handleRemoteInput routes one validated remote instruction to the session
// it targets - the foreground conversation, or a background one this screen
// already tracks in s.sessions (see session.go's sessionState) - using
// EXACTLY the same conv.Send + awaitSessionEvent drain every local send
// already uses. This is the turn-ownership fix: the screen that renders a
// session is the only thing that ever starts and drains a turn for it: see
// docs on internal/uiadapter's SessionPool.RemoteInputs (SessionPool itself
// never calls Send).
//
// The Send call passes context.Background(), identical to every local send
// in this package (see sendTextWithPersisted) - "cancellable" comes from the
// returned TurnHandle's own Cancel, which Conversation.Send derives via its
// own context.WithCancel internally regardless of what ctx the caller passes
// (internal/uiadapter/conversation.go), not from the ctx argument itself.
//
// A target session neither foreground nor tracked in s.sessions (the narrow
// window before this process's first-ever session has been switched away
// from once) cannot be executed: there is no ports.Conversation reference to
// call Send through from here. That refusal is surfaced as a status notice
// on whatever session IS on screen, never silent.
func (s Screen) handleRemoteInput(ev ports.RemoteInputEvent) (app.Screen, tea.Cmd) {
	rearm := s.awaitRemoteInput()
	text := ev.Body
	persisted := remoteInputTagPrefix + ev.Body

	if ev.SessionID == "" || ev.SessionID == s.convID() {
		next, cmd := s.sendOrQueueRemote(text, persisted)
		return next, tea.Batch(rearm, cmd)
	}

	st, ok := s.sessions[ev.SessionID]
	if !ok {
		s.statusline.Notice(fmt.Sprintf("remote input for session %s ignored: session not tracked", ev.SessionID))
		return s, rearm
	}

	if st.active != nil {
		st.queue = append(st.queue, text)
		return s, rearm
	}

	handle, err := st.conv.Send(context.Background(), intent.Send{Text: text, PersistedText: persisted})
	if err != nil {
		st.handleTurnEvent(uievent.Event{
			Kind: uievent.KindError,
			Body: uievent.ErrorBody{Text: fmt.Sprintf("remote send failed: %v", err), Fatal: false},
		})
		return s, rearm
	}
	st.active = handle
	st.statusline.Start("thinking", s.now())
	return s, tea.Batch(rearm, s.awaitSessionEvent(ev.SessionID, handle.Events()))
}

// sendOrQueueRemote sends text as the foreground turn, or queues it behind
// the currently active one - the same fork Screen.send applies to a
// composer submission (see events.go), reused here so a remote instruction
// competes for the turn slot exactly like a local one.
func (s Screen) sendOrQueueRemote(text, persisted string) (app.Screen, tea.Cmd) {
	if s.active != nil {
		s.queue = append(s.queue, text)
		if s.queueOverlay.Active() {
			s.queueOverlay.SetItems(s.queue)
		}
		s.statusline.Notice(fmt.Sprintf("remote message queued (%d in queue)", len(s.queue)))
		return s, nil
	}
	return s.sendTextWithPersisted(text, persisted)
}
