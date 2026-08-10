package cli

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleMouseMsg updates selection/scroll from mouse input.
// Returns true when the welcome-mode branch fully handled the event.
func (m *tuiModel) handleMouseMsg(msg tea.MouseMsg, skipViewport *bool) bool {
	if m.mode == modeWelcome {
		return m.handleWelcomeMouse(msg)
	}
	if m.handleSidebarMouse(msg, skipViewport) {
		return true
	}
	pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	if pane.sidebarVisible && msg.X >= pane.sidebarWidth && msg.X < pane.chatX {
		// The divider and its padding are not interactive panes.
		*skipViewport = true
		return true
	}
	if pane.rightSidebarVisible {
		rightX := pane.rightSidebarX()
		if msg.X >= rightX-sidebarDividerLanes && msg.X < rightX {
			// The right divider and its padding are not interactive panes.
			*skipViewport = true
			return true
		}
	}
	return m.handleHitMapMouse(msg, skipViewport)
}

// handleWelcomeMouse applies one mouse event on the welcome screen. It always
// returns true because the welcome-mode branch fully handled the event.
func (m *tuiModel) handleWelcomeMouse(msg tea.MouseMsg) bool {
	if msg.Type == tea.MouseWheelUp {
		if m.sessionSel > 0 {
			m.sessionSel--
		}
	} else if msg.Type == tea.MouseWheelDown {
		if m.sessionSel < len(m.sessions)-1 {
			m.sessionSel++
		}
	} else if msg.Type == tea.MouseLeft {
		idx := m.sessionIndexAtY(msg.Y)
		if idx >= 0 {
			now := time.Now()
			if idx == m.lastClickIdx && now.Sub(m.lastClickAt) < 400*time.Millisecond {
				m.sessionSel = idx
				if err := m.openSelectedSession(); err == nil {
					m.textarea.Placeholder = composerPlaceholder
				} else {
					m.welcomeNotice = "open failed: " + err.Error()
				}
				m.lastClickIdx = -1
			} else {
				m.sessionSel = idx
				m.lastClickIdx = idx
				m.lastClickAt = now
			}
		}
	}
	return true
}

// handleHitMapMouse routes chat-mode mouse input through the hit map:
// thinking-block wheel scroll, transcript wheel/click, and composer focus.
// Returns false because nothing below the hit map should consume the event.
func (m *tuiModel) handleHitMapMouse(msg tea.MouseMsg, skipViewport *bool) bool {
	zone, hit := m.hitMap.hit(msg.Y)
	if hit && zone.blockID != "" && (msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown) {
		dir := 1
		if msg.Type == tea.MouseWheelUp {
			dir = -1
		}
		if m.adjustThinkingScroll(zone.blockID, dir) {
			m.renderVP()
			*skipViewport = true
			m.noteUserScrolledUp()
			return false
		}
	}
	if hit && zone.kind == hitTranscript && msg.Type == tea.MouseWheelUp {
		m.viewport.ScrollUp(m.viewport.MouseWheelDelta)
		m.noteUserScrolledUp()
		*skipViewport = true
	}
	if hit && zone.kind == hitTranscript && msg.Type == tea.MouseWheelDown {
		m.viewport.ScrollDown(m.viewport.MouseWheelDelta)
		if m.viewport.AtBottom() {
			m.followOutput = true
		}
		*skipViewport = true
	}
	if hit && zone.kind == hitTranscript && msg.Type == tea.MouseLeft {
		if zone.blockID != "" {
			m.handleTranscriptBlockClick(zone.blockID)
		}
	}
	if hit && zone.kind == hitComposer && msg.Type == tea.MouseLeft {
		m.selectedBlockID = ""
		m.lastClickBlockID = ""
		m.setFocus(focusComposer)
		m.renderVP() // clear selection chrome
	}
	return false
}

// handleSidebarMouse routes mouse input in the visible sidebars. Divider
// padding is not part of either pane, so it remains inert. The left region
// drives the sessions sidebar, the right region the workflows sidebar.
func (m *tuiModel) handleSidebarMouse(msg tea.MouseMsg, skipViewport *bool) bool {
	pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	handled := false
	if pane.sidebarVisible && msg.X >= 0 && msg.X < pane.sidebarWidth {
		*skipViewport = true
		m.handleSessionsSidebarMouse(msg, pane.sidebarWidth)
		handled = true
	}
	if pane.rightSidebarVisible && msg.X >= pane.rightSidebarX() {
		*skipViewport = true
		m.handleWorkflowsSidebarMouse(msg, pane.rightSidebarWidth)
		handled = true
	}
	return handled
}

// handleSessionsSidebarMouse applies one mouse event to the sessions sidebar.
func (m *tuiModel) handleSessionsSidebarMouse(msg tea.MouseMsg, width int) {
	sidebar := m.sessionsSidebar
	if sidebar == nil || sidebar.confirm != confirmNone {
		return
	}
	switch msg.Type {
	case tea.MouseWheelUp:
		sidebar.move(m.sessions, -1)
		m.setFocus(focusSidebar)
	case tea.MouseWheelDown:
		sidebar.move(m.sessions, 1)
		m.setFocus(focusSidebar)
	case tea.MouseLeft:
		cursor, ok := sidebar.cursorAt(m.sessions, width, m.height, msg.Y)
		if !ok {
			return
		}
		now := time.Now()
		if sidebar.doubleClick(cursor, now) {
			sidebar.cursor = cursor
			if sidebar.selectsNewSession(m.sessions) {
				m.startNewSession()
			} else if m.workspaceSwitchBusy() {
				m.appendInfo("(finish the current turn before opening a session)")
			} else if session, ok := sidebar.selected(m.sessions); ok {
				if err := m.openSessionInfo(session); err != nil {
					m.appendInfo("open failed: " + err.Error())
					m.renderVP()
				}
			}
		} else {
			sidebar.cursor = cursor
		}
		m.setFocus(focusSidebar)
	}
}

// handleWorkflowsSidebarMouse applies one mouse event to the workflows
// sidebar: wheel moves the cursor, a single click selects the row and takes
// focus, and a double-click on a row selects and opens its run-detail dialog
// (mirroring the sessions sidebar and transcript block double-click pattern).
func (m *tuiModel) handleWorkflowsSidebarMouse(msg tea.MouseMsg, width int) {
	sidebar := m.workflowsSidebar
	if sidebar == nil {
		return
	}
	switch msg.Type {
	case tea.MouseWheelUp:
		sidebar.move(sidebar.rows, -1)
		m.setFocus(focusWorkflowsSidebar)
	case tea.MouseWheelDown:
		sidebar.move(sidebar.rows, 1)
		m.setFocus(focusWorkflowsSidebar)
	case tea.MouseLeft:
		cursor, ok := sidebar.cursorAt(sidebar.rows, width, m.height, msg.Y)
		if !ok {
			return
		}
		now := time.Now()
		if sidebar.doubleClick(cursor, now) {
			sidebar.cursor = cursor
			if row, ok := sidebar.selected(sidebar.rows); ok {
				m.openWorkflowRunDialog(row)
			}
		} else {
			sidebar.cursor = cursor
		}
		m.setFocus(focusWorkflowsSidebar)
	}
}

func mouseWheelDelta(msg tea.MouseMsg) (int, bool) {
	if tea.MouseEvent(msg).IsWheel() {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return -1, true
		case tea.MouseButtonWheelDown:
			return 1, true
		}
	}
	switch msg.Type {
	case tea.MouseWheelUp:
		return -1, true
	case tea.MouseWheelDown:
		return 1, true
	default:
		return 0, false
	}
}

// handleModalMouse is the first mouse router. It consumes every modal event;
// only wheel events are turned into modal navigation. Nothing can fall through
// to transcript copy, hit-map selection, textarea focus, or viewport scrolling.
func (m *tuiModel) handleModalMouse(msg tea.MouseMsg) bool {
	if !m.modalOpen() {
		return false
	}
	delta, wheel := mouseWheelDelta(msg)
	if m.overlay != nil && m.overlay.prefs.pager && wheel {
		layout := m.overlay.layout(max(1, m.width), max(1, m.height))
		m.overlay.renderedRows = m.overlay.rowsForLayout(max(1, layout.innerW), layout.pageH)
		m.overlay.scroll(delta*max(1, m.viewport.MouseWheelDelta), max(1, layout.pageH))
	}
	if m.modelDlg != nil {
		if wheel {
			m.modelDlg.move(delta * max(1, m.viewport.MouseWheelDelta))
		}
		if msg.Type == tea.MouseLeft || msg.Button == tea.MouseButtonLeft {
			if row, ok := m.modelDlg.rowAtY(msg.Y, max(1, m.width), max(1, m.height)); ok {
				for i, candidate := range m.modelDlg.rows {
					if candidate == row && !candidate.header {
						m.modelDlg.cursor = i
						break
					}
				}
			}
		}
	}
	if m.worktreeDlg != nil && wheel {
		visible := m.worktreeDlg.visibleRows(max(1, m.width), max(1, m.height))
		m.worktreeDlg.move(delta * max(1, m.viewport.MouseWheelDelta))
		m.worktreeDlg.clampScrollTo(visible)
	}
	if m.workflowRunDlg != nil && wheel {
		m.workflowRunDlg.move(delta*max(1, m.viewport.MouseWheelDelta), max(1, m.width), max(1, m.height))
	}
	return true
}

// handleTranscriptBlockClick selects a chat block (or work: group). A second
// click on the same ID within 400ms activates (toggle collapse) - same as Enter.
// Root cause of “double-click does nothing”: mouse only set selection before.
func (m *tuiModel) handleTranscriptBlockClick(blockID string) {
	const doubleClick = 400 * time.Millisecond
	now := time.Now()
	// Double-click (or second click on same id within window) → activate.
	if blockID == m.lastClickBlockID && now.Sub(m.lastClickAt) < doubleClick {
		m.selectedBlockID = blockID
		m.setFocus(focusScrollback)
		_ = m.toggleSelectedBlock() // renderVP inside when successful
		m.lastClickBlockID = ""
		m.lastClickAt = time.Time{}
		// If toggle no-op'd (divider), still refresh chrome.
		if m.selectedBlockID == blockID {
			m.renderVP()
		}
		return
	}
	// First click: select + chrome only.
	m.selectedBlockID = blockID
	m.setFocus(focusScrollback)
	m.lastClickBlockID = blockID
	m.lastClickAt = now
	m.renderVP()
}
