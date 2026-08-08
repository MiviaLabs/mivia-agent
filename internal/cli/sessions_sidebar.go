package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/lipgloss"
)

type sessionsConfirm int

const (
	confirmNone sessionsConfirm = iota
	confirmDeleteOne
	confirmPurgeAll
)

func sessionDeleteNotice(info chat.SessionInfo) string {
	if info.WorktreeRoute {
		return "remove worktree sessions with /worktrees"
	}
	return "open this session in its workspace to delete it"
}

// sessionsSidebar stores the session-list state for the left sidebar.
type sessionsSidebar struct {
	cursor          int
	scroll          int
	confirm         sessionsConfirm
	notice          string
	lastClickCursor int
	lastClickAt     time.Time
}

const (
	sidebarNewSessionY = 1
	sidebarRowsY       = 3
	sidebarChromeRows  = 4 // title, new action, divider, and footer
)

// sidebarLayout defines the rows shared by rendering and mouse hit testing.
type sidebarLayout struct {
	newSessionY  int
	rowsY        int
	rowHeight    int
	rowCapacity  int
	showMetadata bool
	showCue      bool
}

func newSidebarLayout(rows, width, height int) sidebarLayout {
	height = max(1, height)
	space := max(0, height-sidebarChromeRows)
	layout := sidebarLayout{
		newSessionY: sidebarNewSessionY,
		rowsY:       sidebarRowsY,
		rowHeight:   1,
		rowCapacity: space,
	}
	if width >= 24 && height >= 10 && space >= 4 {
		layout.rowHeight = 2
		layout.showMetadata = true
		layout.rowCapacity = max(1, space/layout.rowHeight)
	}
	if rows > layout.rowCapacity && space > layout.rowHeight {
		layout.showCue = true
		layout.rowCapacity = max(1, (space-1)/layout.rowHeight)
	}
	return layout
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

// cursorAt returns the sidebar cursor at a rendered terminal row.
func (s *sessionsSidebar) cursorAt(rows []chat.SessionInfo, width, height, y int) (int, bool) {
	if y < 0 || y >= max(1, height) {
		return 0, false
	}
	layout := newSidebarLayout(len(rows), width, height)
	if y == layout.newSessionY && y < max(1, height) {
		return 0, true
	}
	if layout.rowCapacity == 0 {
		return 0, false
	}
	s.clampScroll(len(rows), layout.rowCapacity)
	if y < layout.rowsY {
		return 0, false
	}
	row := (y - layout.rowsY) / layout.rowHeight
	index := s.scroll + row
	if row >= layout.rowCapacity || index < s.scroll || index >= min(len(rows), s.scroll+layout.rowCapacity) {
		return 0, false
	}
	return index + 1, true
}

// doubleClick reports a second click on the same row within the activation window.
func (s *sessionsSidebar) doubleClick(cursor int, now time.Time) bool {
	double := cursor == s.lastClickCursor && now.Sub(s.lastClickAt) < 400*time.Millisecond
	if double {
		s.lastClickCursor = -1
		return true
	}
	s.lastClickCursor = cursor
	s.lastClickAt = now
	return false
}

// view renders the non-modal session picker without an active-session marker.
func (s *sessionsSidebar) view(rows []chat.SessionInfo, width, height int, focused bool) string {
	return s.viewWithActive(rows, width, height, focused, nil)
}

// viewWithActive renders the non-modal session picker. The model owns the rows.
func (s *sessionsSidebar) viewWithActive(rows []chat.SessionInfo, width, height int, focused bool, active *chat.SessionInfo) string {
	width = max(1, width)
	height = max(1, height)
	layout := newSidebarLayout(len(rows), width, height)
	s.move(rows, 0)
	if layout.rowCapacity > 0 {
		s.clampScroll(len(rows), layout.rowCapacity)
	}
	end := min(len(rows), s.scroll+max(0, layout.rowCapacity))

	title := fmt.Sprintf(" Sessions %d · saved sessions", len(rows))
	lines := []string{
		tuiHeaderStyle.Render(sidebarPad(title, width)),
		s.renderNewSession(rows, width, focused),
		tuiDimStyle.Render(strings.Repeat("─", width)),
	}
	if len(rows) == 0 && layout.rowCapacity > 0 {
		lines = append(lines, tuiDimStyle.Render(sidebarPad(" no saved sessions", width)))
	} else {
		latestAuto := latestAutoSaveName(rows)
		for i := s.scroll; i < end; i++ {
			rowLines := s.renderSessionRow(rows[i], i+1 == s.cursor, width, focused, layout.showMetadata, sidebarSessionMatches(rows[i], active), latestAuto)
			lines = append(lines, rowLines...)
		}
	}
	if layout.showCue {
		lines = append(lines, tuiDimStyle.Render(sidebarPad(fmt.Sprintf(" %d–%d / %d", s.scroll+1, end, len(rows)), width)))
	}
	footer := sidebarPad(s.footer(rows), width)
	switch {
	case s.confirm != confirmNone:
		lines = append(lines, tuiErrorStyle.Render(footer))
	case s.notice != "":
		lines = append(lines, tuiInfoStyle.Render(footer))
	default:
		lines = append(lines, tuiDimStyle.Render(footer))
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (s *sessionsSidebar) renderNewSession(rows []chat.SessionInfo, width int, focused bool) string {
	line := "  + New session"
	if s.selectsNewSession(rows) {
		line = "▸ + New session"
		return sidebarSelectedStyle(width, focused).Render(sidebarPad(line, width))
	}
	return tuiAccentStyle.Render(sidebarPad(line, width))
}

func (s *sessionsSidebar) renderSessionRow(row chat.SessionInfo, selected bool, width int, focused, showMetadata, active bool, latestAuto string) []string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	name := displaySessionName(row, latestAuto)
	if active {
		name = "● current · " + name
	}
	line := marker + truncateToWidth(name, max(1, width-runeWidth(marker)))
	line = sidebarPad(line, width)
	if selected {
		line = sidebarSelectedStyle(width, focused).Render(line)
	}
	lines := []string{line}
	if showMetadata {
		metadata := fmt.Sprintf("   %d messages", row.MessageCount)
		if age := formatSessionAge(row.UpdatedAt); age != "" {
			metadata += " · " + age
		}
		lines = append(lines, tuiDimStyle.Render(sidebarPad(metadata, width)))
	}
	return lines
}

func sidebarSessionMatches(row chat.SessionInfo, active *chat.SessionInfo) bool {
	return active != nil && row.Reference() == active.Reference() && row.Dir == active.Dir &&
		row.WorktreeRoute == active.WorktreeRoute && row.WorktreeInstance == active.WorktreeInstance
}

func sidebarSelectedStyle(width int, focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Background(lipgloss.Color(themeColorSelBg)).Width(width)
	if focused {
		return style.Foreground(lipgloss.Color(themeColorBright)).Bold(true)
	}
	return style
}

func sidebarPad(text string, width int) string {
	text = truncateToWidth(text, width)
	return text + strings.Repeat(" ", max(0, width-runeWidth(text)))
}

func (s *sessionsSidebar) footer(rows []chat.SessionInfo) string {
	if s.confirm == confirmDeleteOne {
		return " delete selected session? y/n"
	}
	if s.confirm == confirmPurgeAll {
		return " purge all sessions? y/n"
	}
	if s.notice != "" {
		return " " + s.notice
	}
	if s.selectsNewSession(rows) {
		return " Enter new · Esc close"
	}
	return " Enter open · d delete · P purge"
}
