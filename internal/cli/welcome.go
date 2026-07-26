package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// screenMode is the top-level TUI screen.
type screenMode int

const (
	modeWelcome screenMode = iota
	modeChat
)

// logoTickMsg advances the welcome logo animation.
type logoTickMsg struct{}

// sessionRowHit maps an absolute screen Y to a session list index.
type sessionRowHit struct {
	y0, y1 int // inclusive
	idx    int
}

func logoTickCmd() tea.Cmd {
	// ~12.5 FPS with 24 pre-rasterized braille frames (~1.9s loop).
	// Frames are precomputed once; tick only advances an index (cheap).
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return logoTickMsg{}
	})
}

// displaySessionName renames reserved auto-save for humans.
func displaySessionName(name string) string {
	if name == chat.AutoSaveName {
		return "Last session"
	}
	return name
}

// formatSessionAge returns a short relative time.
func formatSessionAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// renderSessionPicker builds the session list and mouse hit rows.
// yBase is the absolute screen Y of the first line of the returned block.
func renderSessionPicker(
	sessions []chat.SessionInfo,
	selected int,
	scroll int,
	width int,
	maxRows int,
	yBase int,
) (block string, hits []sessionRowHit, newScroll int) {
	if width < 20 {
		width = 20
	}
	if maxRows < 3 {
		maxRows = 3
	}
	title := tuiAccentStyle.Render("  Sessions")
	hint := tuiDimStyle.Render("  ↑↓ select · enter open · click · type + enter new chat")

	if len(sessions) == 0 {
		empty := tuiDimStyle.Render("  No saved sessions yet — type a message and press Enter")
		return strings.Join([]string{title, "", empty, "", hint}, "\n"), nil, 0
	}

	if selected < 0 {
		selected = 0
	}
	if selected >= len(sessions) {
		selected = len(sessions) - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if selected < scroll {
		scroll = selected
	}
	if selected >= scroll+maxRows {
		scroll = selected - maxRows + 1
	}
	if scroll > len(sessions)-maxRows {
		scroll = max(0, len(sessions)-maxRows)
	}
	newScroll = scroll

	var lines []string
	lines = append(lines, title, "")

	rowY := yBase + 2 // title + blank
	end := scroll + maxRows
	if end > len(sessions) {
		end = len(sessions)
	}
	for i := scroll; i < end; i++ {
		si := sessions[i]
		name := displaySessionName(si.Name)
		if len(name) > 28 {
			name = name[:25] + "…"
		}
		meta := fmt.Sprintf("%d msgs · %s", si.MessageCount, formatSessionAge(si.UpdatedAt))
		if si.Name == chat.AutoSaveName {
			meta += " · auto"
		}
		prefix := "  "
		style := tuiDimStyle
		if i == selected {
			prefix = "▸ "
			style = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("236")).
				Bold(true)
		}
		nameCol := fmt.Sprintf("%-28s", name)
		line := style.Render(prefix + nameCol + "  " + meta)
		lines = append(lines, line)
		hits = append(hits, sessionRowHit{y0: rowY, y1: rowY, idx: i})
		rowY++
	}
	if scroll > 0 || end < len(sessions) {
		more := tuiDimStyle.Render(fmt.Sprintf("  (%d–%d of %d)", scroll+1, end, len(sessions)))
		lines = append(lines, more)
	}
	lines = append(lines, "", hint)
	return strings.Join(lines, "\n"), hits, newScroll
}

// hydrateHistory loads the last ~100 conversational messages into the viewport.
func (m *tuiModel) hydrateHistory() {
	m.messages = nil
	m.msgOffset = 0
	msgCount := len(m.session.Messages)
	if msgCount == 0 {
		m.renderVP()
		return
	}
	start := 0
	count := 0
	for i := msgCount - 1; i >= 0 && count < 100; i-- {
		role := m.session.Messages[i].Role
		if role == provider.RoleUser || role == provider.RoleAssistant {
			count++
		}
		start = i
	}
	m.msgOffset = start
	if start > 0 {
		m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  (showing last %d messages, scroll up for more)", count)))
	}
	wrapW := 78
	if m.width > 4 {
		wrapW = m.width - 4
	}
	lines := RenderHistoryMessages(m.session.Messages[start:], m.modelName, wrapW)
	for _, l := range lines {
		m.appendMsg(l)
	}
	m.renderVP()
}

// enterChatMode leaves the welcome screen and shows chat chrome.
func (m *tuiModel) enterChatMode() {
	m.mode = modeChat
	m.sessionHits = nil
	m.layout()
	m.renderVP()
}

// beginNewSession clears in-memory history for a fresh conversation (disk sessions kept).
func (m *tuiModel) beginNewSession() {
	m.session.Clear()
	m.messages = nil
	m.msgOffset = 0
	m.pendingQueue = nil
	m.toolRows = nil
	m.toolPanel = toolPanelState{Selected: -1}
	m.streamBuf.Reset()
	m.thinkingBuf.Reset()
	m.thinkingLines = 0
}

// openSelectedSession loads the selected list entry into chat mode.
func (m *tuiModel) openSelectedSession() error {
	if len(m.sessions) == 0 {
		return fmt.Errorf("no sessions")
	}
	if m.sessionSel < 0 || m.sessionSel >= len(m.sessions) {
		return fmt.Errorf("no selection")
	}
	name := m.sessions[m.sessionSel].Name
	if err := m.session.Load(name); err != nil {
		return err
	}
	m.enterChatMode()
	m.hydrateHistory()
	m.appendInfo(fmt.Sprintf("session %q loaded", displaySessionName(name)))
	m.renderVP()
	return nil
}

// sessionIndexAtY returns the session index under absolute mouse Y, or -1.
func (m *tuiModel) sessionIndexAtY(y int) int {
	for _, h := range m.sessionHits {
		if y >= h.y0 && y <= h.y1 {
			return h.idx
		}
	}
	return -1
}
