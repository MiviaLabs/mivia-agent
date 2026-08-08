package cli

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func (m *tuiModel) handleSidebarKey(key string) bool {
	if m.focus != focusSidebar || !m.sidebarVisible() {
		return false
	}
	sidebar := m.sessionsSidebar
	if sidebar.confirm != confirmNone {
		switch key {
		case "y":
			m.applySidebarSessionsConfirm()
		case "n", "esc":
			sidebar.confirm = confirmNone
		}
		return true
	}
	switch key {
	case "esc":
		m.sessionsSidebar = nil
		m.setFocus(focusComposer)
		m.layout()
		m.renderVP()
	case "up", "k":
		sidebar.move(m.sessions, -1)
	case "down", "j":
		sidebar.move(m.sessions, 1)
	case "enter":
		if sidebar.selectsNewSession(m.sessions) {
			m.startNewSession()
			break
		}
		if m.workspaceSwitchBusy() {
			m.appendInfo("(finish the current turn before opening a session)")
			break
		}
		if session, ok := sidebar.selected(m.sessions); ok {
			if err := m.openSessionInfo(session); err != nil {
				m.appendInfo("open failed: " + err.Error())
				m.renderVP()
			}
		}
	case "d":
		if session, ok := sidebar.selected(m.sessions); ok {
			if session.WorktreeRoute {
				sidebar.notice = sessionDeleteNotice(session)
			} else {
				sidebar.confirm = confirmDeleteOne
			}
		}
	case "P":
		for _, session := range m.sessions {
			if !session.WorktreeRoute {
				sidebar.confirm = confirmPurgeAll
				break
			}
		}
	default:
		return false
	}
	return true
}

func (m *tuiModel) applySidebarSessionsConfirm() {
	sidebar := m.sessionsSidebar
	if sidebar == nil {
		return
	}
	switch sidebar.confirm {
	case confirmDeleteOne:
		session, ok := sidebar.selected(m.sessions)
		if !ok {
			break
		}
		if session.WorktreeRoute {
			sidebar.notice = sessionDeleteNotice(session)
			break
		}
		if err := m.session.DeleteSession(session.Name); err != nil {
			sidebar.notice = "delete failed: " + err.Error()
			break
		}
		index := sidebar.cursor - 1
		m.sessions = append(m.sessions[:index], m.sessions[index+1:]...)
		sidebar.move(m.sessions, 0)
		sidebar.notice = fmt.Sprintf("deleted %q", session.Name)
	case confirmPurgeAll:
		remaining := make([]chat.SessionInfo, 0, len(m.sessions))
		deleted, failed := 0, 0
		for _, session := range m.sessions {
			if session.WorktreeRoute {
				remaining = append(remaining, session)
				continue
			}
			if err := m.session.DeleteSession(session.Name); err != nil {
				failed++
				remaining = append(remaining, session)
				continue
			}
			deleted++
		}
		m.sessions = remaining
		sidebar.move(m.sessions, 0)
		if failed > 0 {
			sidebar.notice = fmt.Sprintf("purged %d sessions (%d failed)", deleted, failed)
		} else {
			sidebar.notice = fmt.Sprintf("purged %d sessions", deleted)
		}
	}
	sidebar.confirm = confirmNone
	if m.sessionSel >= len(m.sessions) {
		m.sessionSel = max(0, len(m.sessions)-1)
	}
}
