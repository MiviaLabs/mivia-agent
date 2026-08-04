package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// listSessions returns the repository-wide catalog wired into the session.
func (m *tuiModel) listSessions() ([]chat.SessionInfo, error) {
	return m.session.ListSessions()
}
