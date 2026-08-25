package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type sessionState struct {
	conv       ports.Conversation
	transcript transcript.Model
	active     ports.TurnHandle
	statusline statusline.Model
	approval   approval.Model
	panel      panel
}

func (st *sessionState) handleTurnEvent(ev uievent.Event) {
	st.transcript, _ = st.transcript.HandleEvent(ev)
	switch b := ev.Body.(type) {
	case uievent.ToolPendingBody:
		st.approval.SetRequest(b)
		st.statusline.SetLabel("pending")
		st.statusline.SetDetail(toolDetail(b.Name, b.Args))
		st.panel.dialog, st.panel.dialogAgent = false, ""
	case uievent.ToolStartBody:
		st.approval.Clear()
		st.statusline.SetLabel("running")
		st.statusline.SetDetail(toolDetail(b.Name, b.Args))
		if isSubagentTool(b.Name) {
			st.panel.observeAgentStart(b.ToolCallID, b.Name)
		}
	case uievent.ToolOutputBody:
		if b.Progress != nil {
			st.panel.observeAgent(b.ToolCallID, b.Progress)
		}
	case uievent.ToolEndBody:
		st.approval.Clear()
		st.statusline.SetLabel("thinking")
		st.panel.observeAgentEnd(b.ToolCallID, b.OK)
		if b.Diff != nil {
			st.panel.appendLive(*b.Diff)
		}
	case uievent.UsageBody:
		st.statusline.SetCost(b.CostUSD)
	case uievent.TurnEndBody:
		st.approval.Clear()
		st.panel.reconcileTerminal(b.Reason)
	}
}

func (s Screen) convID() string {
	if s.conv == nil || s.conv.ID() == "" {
		return "default"
	}
	return s.conv.ID()
}

func (s *Screen) switchConversation(newConv ports.Conversation) {
	if newConv == nil {
		return
	}
	if s.sessions == nil {
		s.sessions = make(map[string]*sessionState)
	}

	// Save current session state
	if s.conv != nil {
		oldID := s.convID()
		s.sessions[oldID] = &sessionState{
			conv:       s.conv,
			transcript: s.transcript,
			active:     s.active,
			statusline: s.statusline,
			approval:   s.approval,
			panel:      s.panel,
		}
	}

	s.conv = newConv
	newID := s.convID()

	if st, ok := s.sessions[newID]; ok {
		s.transcript = st.transcript
		s.active = st.active
		s.statusline = st.statusline
		s.approval = st.approval
		s.panel = st.panel
	} else {
		s.transcript = transcript.New(s.Theme, s.Tier)
		s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
		s.active = nil
		s.statusline = statusline.New(s.Theme, s.Tier)
		s.approval = approval.New(s.Theme, s.Tier)
		s.approval.SetWidth(contentWidth(s.width))
		s.panel = newPanel(s.Theme, s.Tier)
		s.LoadHistory(newConv.History())
	}

	s.refreshTopbar()
	s.reflow()
}

func (s Screen) handleEventMsg(msg uievent.EventMsg) (app.Screen, tea.Cmd) {
	if msg.SessionID != "" && s.convID() != msg.SessionID {
		if st, ok := s.sessions[msg.SessionID]; ok {
			st.handleTurnEvent(msg.Event)
			if st.active != nil {
				return s, s.awaitSessionEvent(msg.SessionID, st.active.Events())
			}
		}
		return s, nil
	}
	return s.handleTurnEvent(msg.Event)
}

func (s Screen) handleTurnEndedMsg(msg turnEndedMsg) (app.Screen, tea.Cmd) {
	if msg.sessionID != "" && s.convID() != msg.sessionID {
		if st, ok := s.sessions[msg.sessionID]; ok {
			st.statusline.Stop()
			st.approval.Clear()
			st.panel.reconcileTerminal("interrupted")
			st.active = nil
		}
		return s, nil
	}
	s.statusline.Stop()
	s.approval.Clear()
	s.panel.reconcileTerminal("interrupted")
	s.active = nil
	s.refreshTopbar()
	return s, nil
}
