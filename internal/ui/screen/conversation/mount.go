package conversation

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// sessionMountedMsg is sent when a background session has been loaded/mounted.
type sessionMountedMsg struct {
	sessionID string
	conv      ports.Conversation
	err       error
}

func (s Screen) mountSessionCmd(sessionID string) tea.Cmd {
	if s.mounter == nil {
		return nil
	}
	m := s.mounter
	return func() tea.Msg {
		conv, err := m.Mount(sessionID)
		return sessionMountedMsg{
			sessionID: sessionID,
			conv:      conv,
			err:       err,
		}
	}
}

func (s Screen) handleSessionMountedMsg(msg sessionMountedMsg) (Screen, tea.Cmd) {
	events, exists := s.mounting[msg.sessionID]
	if exists {
		delete(s.mounting, msg.sessionID)
	}

	if msg.err != nil || msg.conv == nil {
		// The buffered inputs die with the failed mount, so the notice
		// leads with them: reporting only the error left the user's remote
		// message silently gone, and the status row is width-truncated, so
		// what was lost has to come before why.
		s.statusline.Notice(droppedRemoteInputNotice(msg.sessionID, msg.err, events))
		return s, nil
	}

	if len(events) == 0 {
		return s, nil
	}

	firstEvent := events[0]
	text := firstEvent.Body
	persisted := remoteInputTagPrefix + firstEvent.Body

	// Race check: if the user switched to this session while mount was in flight
	if msg.sessionID == s.convID() {
		next, cmd := s.sendOrQueueRemote(text, persisted)
		sc := next.(Screen)
		for _, remaining := range events[1:] {
			sc.queue = append(sc.queue, remaining.Body)
		}
		return sc, cmd
	}

	if s.sessions == nil {
		s.sessions = make(map[string]*sessionState)
	}

	st, ok := s.sessions[msg.sessionID]
	if !ok {
		st = s.newSessionState(msg.conv)
		s.sessions[msg.sessionID] = st
	}

	for _, remaining := range events[1:] {
		st.queue = append(st.queue, remaining.Body)
	}

	if st.active != nil {
		st.queue = append(st.queue, text)
		return s, nil
	}

	handle, err := st.conv.Send(context.Background(), intent.Send{Text: text, PersistedText: persisted})
	if err != nil {
		st.handleTurnEvent(uievent.Event{
			Kind: uievent.KindError,
			Body: uievent.ErrorBody{Text: fmt.Sprintf("remote send failed: %v", err), Fatal: false},
		})
		return s, nil
	}
	st.active = handle
	st.statusline.Start("thinking", s.now())
	return s, s.awaitSessionEvent(msg.sessionID, handle.Events())
}

// droppedRemoteInputNotice reports the remote inputs a failed mount
// discarded, naming what was lost before why: the status row is truncated to
// the terminal width, so a trailing detail is the part the user never sees.
// The body is bounded because a remote message is arbitrary length.
func droppedRemoteInputNotice(sessionID string, cause error, events []ports.RemoteInputEvent) string {
	if len(events) == 0 {
		return fmt.Sprintf("session %s failed to mount: %v", sessionID, cause)
	}
	body := events[0].Body
	const maxQuoted = 80
	if len(body) > maxQuoted {
		body = body[:maxQuoted] + "..."
	}
	more := ""
	if len(events) > 1 {
		more = fmt.Sprintf(" (+%d more)", len(events)-1)
	}
	return fmt.Sprintf("remote input dropped for %s: %q%s - mount failed: %v", sessionID, body, more, cause)
}
