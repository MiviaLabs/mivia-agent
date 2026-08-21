package cli

import (
	"fmt"
	"os"
	"path/filepath"
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
	if si.WorktreeRoute {
		return "Worktree · " + si.Worktree
	}
	if !chat.IsAutoSaveName(si.Name) {
		if si.Title != "" {
			return si.Title
		}
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
	hint := TUIDimStyle.Render("  ↑↓ select · enter open · click · type + enter new chat")

	if len(sessions) == 0 {
		empty := TUIDimStyle.Render("  No saved sessions yet - type a message and press Enter")
		return strings.Join([]string{title, "", empty, "", hint}, "\n"), nil, 0
	}

	selected, scroll = normalizePickerSelection(selected, scroll, maxRows, len(sessions))
	newScroll = scroll

	lines, hits, _ := renderSessionRows(sessions, selected, scroll, width, maxRows, yBase)
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

func renderSessionRows(sessions []chat.SessionInfo, selected, scroll, width, maxRows, yBase int) ([]string, []sessionRowHit, int) {
	lines := []string{tuiAccentStyle.Render("  Sessions"), ""}
	hits := make([]sessionRowHit, 0, maxRows)
	latestAuto := latestAutoSaveName(sessions)
	end := min(len(sessions), scroll+maxRows)
	for i := scroll; i < end; i++ {
		si := sessions[i]
		name := displaySessionName(si, latestAuto)
		meta := fmt.Sprintf("%d msgs · %s", si.MessageCount, formatSessionAge(si.UpdatedAt))
		if si.Worktree != "" {
			meta = "⊞ " + si.Worktree + " · " + meta
		}
		if chat.IsAutoSaveName(si.Name) {
			meta += " · auto"
		}
		style, prefix := TUIDimStyle, "  "
		if i == selected {
			prefix = "◆ "
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorBright)).Background(lipgloss.Color(themeColorCardBg)).Bold(true)
		}
		nameWidth := max(1, width-runeWidth(prefix)-runeWidth(meta)-2)
		if runeWidth(name) > nameWidth {
			name = truncateToWidth(name, max(1, nameWidth-1)) + "…"
		}
		line := prefix + name + strings.Repeat(" ", max(0, nameWidth-runeWidth(name))) + "  " + meta
		lines = append(lines, style.Render(truncateToWidth(line, width)))
		hits = append(hits, sessionRowHit{y0: yBase + 2 + i - scroll, y1: yBase + 2 + i - scroll, idx: i})
	}
	if scroll > 0 || end < len(sessions) {
		lines = append(lines, TUIDimStyle.Render(fmt.Sprintf("  (%d–%d of %d)", scroll+1, end, len(sessions))))
	}
	return append(lines, "", TUIDimStyle.Render("  ↑↓ select · enter open · click · type + enter new chat")), hits, end
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
		m.appendMsg(TUIDimStyle.Render(fmt.Sprintf("  (showing last %d messages, scroll up for more)", count)))
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
	if err := m.session.Clear(); err != nil {
		m.appendInfo("session reset failed: " + err.Error())
	}
	m.messages = nil
	m.blocks = nil
	m.msgOffset = 0
	m.resetQueueState()
	m.toolRows = nil
	m.toolPanel = toolPanelState{Selected: -1}
	m.streamBuf.Reset()
	m.thinkingBuf.Reset()
	m.cancelling = false
	m.quitRequested = false
	m.agentDone = false
	m.sentHistory = nil
	m.closeHistory()
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
	return nil
}

// startNewSession applies the shared TUI new-session guard and action.
func (m *tuiModel) startNewSession() {
	if m.workspaceSwitchBusy() {
		m.appendInfo("(finish the current turn before /new)")
		return
	}
	if err := m.resetForNewSession(); err != nil {
		m.appendInfo("new session failed: " + err.Error())
		return
	}
	m.activeSession = nil
	m.appendInfo("new session started (previous conversation saved)")
	if err := m.refreshSessionList(); err != nil {
		m.appendInfo("sessions refresh failed: " + err.Error())
	}
	m.renderVP()
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
	return m.openSessionInfo(si)
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
		if m.sessions[i].Name == name && !m.sessions[i].WorktreeRoute {
			si = &m.sessions[i]
			break
		}
	}
	if si == nil {
		for i := range m.sessions {
			if m.sessions[i].Name == name {
				si = &m.sessions[i]
				break
			}
		}
	}
	if si == nil {
		return fmt.Errorf("session %q not found", name)
	}
	return m.openSessionInfo(*si)
}

// worktreeRouteOpenable reports whether a worktree route row can be opened.
// The sessions sidebar uses the same check to decide whether a broken route
// may be deleted from the session list: openable rows route to /worktrees,
// broken rows are deletable.
func (m *tuiModel) worktreeRouteOpenable(si chat.SessionInfo) (bool, error) {
	if !si.WorktreeInstance.IsZero() {
		if err := m.validateSessionWorktree(si.Dir, si.WorktreeInstance); err != nil {
			return false, err
		}
	}
	dir, err := filepath.Abs(si.Dir)
	if err != nil {
		return false, fmt.Errorf("resolve worktree route: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("worktree route is unavailable: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("worktree route is not a directory")
	}
	return true, nil
}

// openSessionInfo opens the exact selected session. Routes and snapshots can
// have the same display name, so selection cannot use a name alone.
func (m *tuiModel) openSessionInfo(si chat.SessionInfo) error {
	if m.workspaceSwitchBusy() {
		return fmt.Errorf("cannot switch while agent is running")
	}
	if !si.WorktreeInstance.IsZero() {
		if err := m.validateSessionWorktree(si.Dir, si.WorktreeInstance); err != nil {
			return err
		}
	}
	if si.WorktreeRoute {
		if _, err := m.worktreeRouteOpenable(si); err != nil {
			return err
		}
		dir, err := filepath.Abs(si.Dir)
		if err != nil {
			return fmt.Errorf("resolve worktree route: %w", err)
		}
		m.workspaceDir = dir
		m.restartWorkspace = dir
		m.restartWorktreeInstance = si.WorktreeInstance
		return nil
	}
	if si.Dir != "" && !sameSessionWorkspace(m.resolveWorkspaceDir(), si.Dir) {
		dir, err := filepath.Abs(si.Dir)
		if err != nil {
			return fmt.Errorf("resolve session workspace: %w", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("session workspace is unavailable: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("session workspace is not a directory")
		}
		m.workspaceDir = dir
		m.restartWorkspace = dir
		m.resumeSessionName = si.Reference()
		m.restartWorktreeInstance = si.WorktreeInstance
		return nil
	}
	if err := m.session.Load(si.Reference()); err != nil {
		return err
	}
	m.resetQueueState()
	m.sentHistory = nil
	m.closeHistory()
	active := si
	m.activeSession = &active
	m.modelName = shortenModel(m.session.CurrentModel())
	m.enterChatMode()
	m.hydrateHistory()
	m.appendInfo(fmt.Sprintf("session %q loaded", displaySessionName(si, latestAutoSaveName(m.sessions))))
	m.appendModelRestoreNotice()
	// A session records the directory (and mivia worktree) it lived in.
	// Restore it only after the load succeeded, so a failed load can never
	// leave the process in a directory with no session attached.
	if si.Dir != "" {
		m.restoreSessionDir(si.Dir)
	}
	m.renderVP()
	return nil
}

func sameSessionWorkspace(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}

// restoreSessionDir changes the process working directory back to the
// directory a session was created or used in, then refreshes the TUI git
// context so the status bar shows the restored branch and worktree. It
// refuses while an agent turn is in flight and reports a notice when the
// directory no longer exists or cannot be entered, mirroring
// switchToWorktree.
func (m *tuiModel) restoreSessionDir(dir string) {
	if m.waiting {
		m.appendInfo("cannot switch directory while agent is running")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		m.appendInfo(fmt.Sprintf("session directory no longer exists: %s", dir))
		return
	}
	if err := os.Chdir(dir); err != nil {
		m.appendInfo("switch failed: " + err.Error())
		return
	}
	m.workspaceDir = shortenWorkspacePath()
	m.refreshGitContext()
	m.appendInfo(fmt.Sprintf("switched to %s", shortenWorkspacePath()))
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
