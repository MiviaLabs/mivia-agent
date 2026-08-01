package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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

// periodicSaveMsg triggers an auto-save tick during long chat sessions.
// Fires every 60 seconds while in chat mode to prevent data loss on crash.
type periodicSaveMsg struct{}

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

// periodicSaveCmd returns a command that fires periodicSaveMsg every 60 seconds
// to auto-save the current session during long conversations.
func periodicSaveCmd() tea.Cmd {
	return tea.Tick(60*time.Second, func(time.Time) tea.Msg {
		return periodicSaveMsg{}
	})
}

// latestAutoSaveName returns the most recently updated auto-save name in infos
// (ListSessions order is newest-first; first auto match wins).
func latestAutoSaveName(infos []chat.SessionInfo) string {
	for _, si := range infos {
		if chat.IsAutoSaveName(si.Name) {
			return si.Name
		}
	}
	return ""
}

// displaySessionName labels sessions for the welcome picker.
// Latest auto-save → "Last session"; older autos → "Auto · {relative time}";
// named sessions keep their name. Handles bare __last__ and __last__* names.
func displaySessionName(si chat.SessionInfo, latestAuto string) string {
	if !chat.IsAutoSaveName(si.Name) {
		return si.Name
	}
	if latestAuto != "" && si.Name == latestAuto {
		return "Last session"
	}
	// Single auto without explicit latest, or matching legacy bare name as sole latest.
	if latestAuto == "" {
		return "Last session"
	}
	age := formatSessionAge(si.UpdatedAt)
	if age != "" {
		return "Auto · " + age
	}
	return "Auto"
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
	width, maxRows = normalizePickerSize(width, maxRows)
	title := tuiAccentStyle.Render("  Sessions")
	hint := tuiDimStyle.Render("  ↑↓ select · enter open · click · type + enter new chat")

	if len(sessions) == 0 {
		empty := tuiDimStyle.Render("  No saved sessions yet - type a message and press Enter")
		return strings.Join([]string{title, "", empty, "", hint}, "\n"), nil, 0
	}

	selected, scroll = normalizePickerSelection(selected, scroll, maxRows, len(sessions))
	newScroll = scroll

	lines, hits, _ := renderSessionRows(sessions, selected, scroll, maxRows, yBase)
	return strings.Join(lines, "\n"), hits, newScroll
}

func normalizePickerSize(width, maxRows int) (int, int) {
	return max(20, width), max(1, maxRows)
}

func normalizePickerSelection(selected, scroll, maxRows, sessionCount int) (int, int) {
	selected = min(max(selected, 0), sessionCount-1)
	scroll = max(0, scroll)
	if selected < scroll {
		scroll = selected
	}
	if selected >= scroll+maxRows {
		scroll = selected - maxRows + 1
	}
	return selected, min(scroll, max(0, sessionCount-maxRows))
}

func renderSessionRows(sessions []chat.SessionInfo, selected, scroll, maxRows, yBase int) ([]string, []sessionRowHit, int) {
	lines := []string{tuiAccentStyle.Render("  Sessions"), ""}
	hits := make([]sessionRowHit, 0, maxRows)
	latestAuto := latestAutoSaveName(sessions)
	end := min(len(sessions), scroll+maxRows)
	for i := scroll; i < end; i++ {
		si := sessions[i]
		name := displaySessionName(si, latestAuto)
		if len(name) > 28 {
			name = name[:25] + "…"
		}
		meta := fmt.Sprintf("%d msgs · %s", si.MessageCount, formatSessionAge(si.UpdatedAt))
		if chat.IsAutoSaveName(si.Name) {
			meta += " · auto"
		}
		style, prefix := tuiDimStyle, "  "
		if i == selected {
			prefix = "◆ "
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorBright)).Background(lipgloss.Color(themeColorCardBg)).Bold(true)
		}
		lines = append(lines, style.Render(prefix+fmt.Sprintf("%-28s  %s", name, meta)))
		hits = append(hits, sessionRowHit{y0: yBase + 2 + i - scroll, y1: yBase + 2 + i - scroll, idx: i})
	}
	if scroll > 0 || end < len(sessions) {
		lines = append(lines, tuiDimStyle.Render(fmt.Sprintf("  (%d–%d of %d)", scroll+1, end, len(sessions))))
	}
	return append(lines, "", tuiDimStyle.Render("  ↑↓ select · enter open · click · type + enter new chat")), hits, end
}

// hydrateHistory loads the last ~100 conversational messages into the viewport.
func (m *tuiModel) hydrateHistory() {
	m.messages = nil
	m.blocks = nil
	m.msgOffset = 0
	msgs := m.session.MessagesCopy()
	msgCount := len(msgs)
	if msgCount == 0 {
		m.renderVP()
		return
	}
	start := 0
	count := 0
	for i := msgCount - 1; i >= 0 && count < 100; i-- {
		role := msgs[i].Role
		if role == provider.RoleUser || role == provider.RoleAssistant {
			count++
		}
		start = i
	}
	m.msgOffset = start
	if start > 0 {
		m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  (showing last %d messages, scroll up for more)", count)))
	}
	for _, block := range HydrateChatBlocksForView(msgs[start:]) {
		m.appendBlock(block)
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
	m.blocks = nil
	m.msgOffset = 0
	m.pendingQueue = nil
	m.pendingQueueLabels = nil
	m.pendingSkillTurns = nil
	m.toolRows = nil
	m.toolPanel = toolPanelState{Selected: -1}
	m.streamBuf.Reset()
	m.thinkingBuf.Reset()
	m.cancelling = false
	m.quitRequested = false
	m.agentDone = false
}

// resetForNewSession starts a fresh conversation while preserving the old one
// on disk. Unlike /clear (which wipes history and lets the next turn clobber
// the single rolling _turn_ snapshot), /new persists the outgoing chat as a
// distinct __last__ exit snapshot, mints a fresh ledger identity, and rebuilds
// the SaveManager so the new session's per-turn writes do not overwrite the
// old session's rolling snapshot.
//
// Caller MUST guarantee no agent turn is in flight (m.waiting == false in the
// TUI): SaveLast and SetSessionStore both touch fields the agent goroutine
// reads without a lock during a turn's commitTurnHistory path.
func (m *tuiModel) resetForNewSession() error {
	_ = m.session.SaveLast()
	newID, err := m.session.RotateSessionID()
	if err != nil {
		return err
	}
	setActiveSessionCaller(runtime.Caller{SessionID: newID})
	if store, ok := m.session.Store().(*chat.FileSessionStore); ok && store != nil {
		mgr := chat.NewSaveManager(store, m.session.CurrentModel(), m.session.Completer.Name())
		m.session.SetSessionStore(store, mgr)
	}
	m.beginNewSession()
	m.refreshSessionList()
	return nil
}

// openSelectedSession loads the selected list entry into chat mode.
func (m *tuiModel) openSelectedSession() error {
	if len(m.sessions) == 0 {
		return fmt.Errorf("no sessions")
	}
	if m.sessionSel < 0 || m.sessionSel >= len(m.sessions) {
		return fmt.Errorf("no selection")
	}
	si := m.sessions[m.sessionSel]
	return m.openSessionByName(si.Name)
}

// openSessionByName loads the session with the given name into chat mode.
// It finds the session info in m.sessions, loads it from disk, and transitions to chat mode.
// If the name is empty or not found, it returns an error and is a no-op.
func (m *tuiModel) openSessionByName(name string) error {
	if name == "" {
		return fmt.Errorf("empty session name")
	}
	var si *chat.SessionInfo
	for i := range m.sessions {
		if m.sessions[i].Name == name {
			si = &m.sessions[i]
			break
		}
	}
	if si == nil {
		return fmt.Errorf("session %q not found", name)
	}
	if err := m.session.Load(si.Name); err != nil {
		return err
	}
	m.modelName = shortenModel(m.session.CurrentModel())
	m.enterChatMode()
	m.hydrateHistory()
	m.appendInfo(fmt.Sprintf("session %q loaded", displaySessionName(*si, latestAutoSaveName(m.sessions))))
	m.appendModelRestoreNotice()
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
