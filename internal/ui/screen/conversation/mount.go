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
		s.statusline.Notice(fmt.Sprintf("remote input for session %s failed to mount: %v", msg.sessionID, msg.err))
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
