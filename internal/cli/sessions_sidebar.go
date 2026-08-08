package cli

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/lipgloss"
)

// sessionsSidebar stores the session-list state for the left sidebar.
type sessionsSidebar struct {
	cursor  int
	scroll  int
	confirm sessionsConfirm
	notice  string
}

func (s *sessionsSidebar) clampScroll(rows int, visible int) {
	visible = max(1, visible)
	if s.cursor == 0 || rows <= visible {
		s.scroll = 0
		return
	}
	cursor := s.cursor - 1
	if cursor < s.scroll {
		s.scroll = cursor
	}
	if cursor >= s.scroll+visible {
		s.scroll = cursor - visible + 1
	}
	s.scroll = min(max(0, s.scroll), rows-visible)
}

func newSessionsSidebar() *sessionsSidebar {
	return &sessionsSidebar{}
}

func (s *sessionsSidebar) move(rows []chat.SessionInfo, delta int) {
	last := len(rows)
	cursor := s.cursor
	if cursor < 0 {
		cursor = 0
	} else if cursor > last {
		cursor = last
	}
	if delta > 0 {
		if delta > last-cursor {
			cursor = last
		} else {
			cursor += delta
		}
	} else if delta < 0 {
		if delta < -cursor {
			cursor = 0
		} else {
			cursor += delta
		}
	}
	s.cursor = cursor
}

// selectsNewSession reports if the permanent first row is selected.
func (s *sessionsSidebar) selectsNewSession(rows []chat.SessionInfo) bool {
	s.move(rows, 0)
	return s.cursor == 0
}

func (s *sessionsSidebar) selected(rows []chat.SessionInfo) (chat.SessionInfo, bool) {
	s.move(rows, 0)
	if s.cursor == 0 || len(rows) == 0 {
		return chat.SessionInfo{}, false
	}
	return rows[s.cursor-1], true
}

// view renders the non-modal session picker. The model owns the session rows.
func (s *sessionsSidebar) view(rows []chat.SessionInfo, width, height int, focused bool) string {
	width = max(1, width)
	height = max(1, height)
	const chromeRows = 4 // title + new action + divider + controls
	visible := max(1, height-chromeRows)
	title := tuiHeaderStyle.Render(" sessions ")
	newSession := "  New session"
	if s.selectsNewSession(rows) {
		newSession = "▸ New session"
		if focused {
			newSession = lipgloss.NewStyle().Bold(true).Render(newSession)
		}
	}
	divider := tuiDimStyle.Render(strings.Repeat("─", width))
	lines := []string{title, newSession, divider}
	if len(rows) == 0 {
		lines = append(lines, tuiDimStyle.Render(" no saved sessions"))
	} else {
		s.move(rows, 0)
		s.clampScroll(len(rows), visible)
		end := min(len(rows), s.scroll+visible)
		for i := s.scroll; i < end; i++ {
			row := rows[i]
			marker := "  "
			name := row.Name
			if i+1 == s.cursor {
				marker = "▸ "
				if focused {
					name = lipgloss.NewStyle().Bold(true).Render(name)
				}
			}
			lines = append(lines, marker+truncateToWidth(name, max(1, width-2)))
		}
	}
	lines = append(lines, s.footer())
	for i := range lines {
		lines[i] = truncateToWidth(lines[i], width)
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (s *sessionsSidebar) footer() string {
	if s.confirm == confirmDeleteOne {
		return tuiErrorStyle.Render(" delete selected session? y/n")
	}
	if s.confirm == confirmPurgeAll {
		return tuiErrorStyle.Render(" purge all sessions? y/n")
	}
	if s.notice != "" {
		return tuiDimStyle.Render(" " + s.notice)
	}
	return tuiDimStyle.Render(" ↑↓ move · enter open/start · d delete · P purge · esc close")
}
