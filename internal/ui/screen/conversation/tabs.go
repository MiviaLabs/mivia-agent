package conversation

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

func (s *Screen) refreshTabs() {
	if len(s.sessionOrder) > 1 {
		var tabs []topbar.SessionTab
		currID := s.convID()
		for i, id := range s.sessionOrder {
			idx := i + 1
			if id == currID {
				title := ""
				if s.conv != nil {
					title = s.conv.Title()
				}
				tabs = append(tabs, topbar.SessionTab{
					ID:          id,
					Title:       title,
					Index:       idx,
					IsCurrent:   true,
					Running:     s.active != nil,
					NeedsAction: s.approval.Active(),
				})
			} else if st, ok := s.sessions[id]; ok {
				title := ""
				if st.conv != nil {
					title = st.conv.Title()
				}
				tabs = append(tabs, topbar.SessionTab{
					ID:          id,
					Title:       title,
					Index:       idx,
					IsCurrent:   false,
					Running:     st.active != nil,
					NeedsAction: st.approval.Active(),
				})
			}
		}
		s.topbar.SetTabs(tabs)
	} else {
		s.topbar.SetTabs(nil)
	}
}

func (s *Screen) registerSession(id string) {
	if id == "" {
		id = "default"
	}
	for _, existing := range s.sessionOrder {
		if existing == id {
			return
		}
	}
	s.sessionOrder = append(s.sessionOrder, id)
}

func (s Screen) switchToSessionID(id string) (app.Screen, tea.Cmd) {
	if id == "" || id == s.convID() {
		return s, nil
	}
	if st, ok := s.sessions[id]; ok && st.conv != nil {
		s.switchConversation(st.conv)
		return s, tea.ClearScreen
	}
	if s.runner != nil {
		out := s.runner.SelectSession(context.Background(), id)
		next, outcomeCmd := s.applyCommandOutcome(out)
		return next, tea.Batch(outcomeCmd, tea.ClearScreen)
	}
	return s, nil
}

func (s Screen) switchToSessionIndex(idx int) (app.Screen, tea.Cmd) {
	if idx < 0 || idx >= len(s.sessionOrder) {
		return s, nil
	}
	return s.switchToSessionID(s.sessionOrder[idx])
}

func (s Screen) switchTabRelative(delta int) (app.Screen, tea.Cmd) {
	if len(s.sessionOrder) <= 1 {
		return s, nil
	}
	currIdx := 0
	currID := s.convID()
	for i, id := range s.sessionOrder {
		if id == currID {
			currIdx = i
			break
		}
	}
	nextIdx := (currIdx + delta) % len(s.sessionOrder)
	if nextIdx < 0 {
		nextIdx += len(s.sessionOrder)
	}
	return s.switchToSessionIndex(nextIdx)
}

func (s Screen) tabGlobalAction(id keymap.ID) (app.Screen, tea.Cmd, bool) {
	switch id {
	case keymap.IDTabPrev:
		next, cmd := s.switchTabRelative(-1)
		return next, cmd, true
	case keymap.IDTabNext:
		next, cmd := s.switchTabRelative(1)
		return next, cmd, true
	case keymap.IDTab1:
		next, cmd := s.switchToSessionIndex(0)
		return next, cmd, true
	case keymap.IDTab2:
		next, cmd := s.switchToSessionIndex(1)
		return next, cmd, true
	case keymap.IDTab3:
		next, cmd := s.switchToSessionIndex(2)
		return next, cmd, true
	case keymap.IDTab4:
		next, cmd := s.switchToSessionIndex(3)
		return next, cmd, true
	case keymap.IDTab5:
		next, cmd := s.switchToSessionIndex(4)
		return next, cmd, true
	case keymap.IDTab6:
		next, cmd := s.switchToSessionIndex(5)
		return next, cmd, true
	case keymap.IDTab7:
		next, cmd := s.switchToSessionIndex(6)
		return next, cmd, true
	case keymap.IDTab8:
		next, cmd := s.switchToSessionIndex(7)
		return next, cmd, true
	case keymap.IDTab9:
		next, cmd := s.switchToSessionIndex(8)
		return next, cmd, true
	default:
		return s, nil, false
	}
}
