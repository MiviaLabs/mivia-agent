package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleWelcomeKey handles key events in welcome mode.
// Returns true if the key was consumed (skipTextarea).
func (m *tuiModel) handleWelcomeKey(key string) bool {
	composerEmpty := strings.TrimSpace(m.textarea.Value()) == ""
	nav := false
	switch key {
	case "up", "down":
		nav = true
	case "k", "j":
		nav = composerEmpty
	case "pgup", "pgdown", "home", "end":
		nav = true
	case "ctrl+o":
		if name := latestAutoSaveName(m.sessions); name != "" {
			if err := m.openSessionByName(name); err == nil {
				m.textarea.Placeholder = composerPlaceholder
			} else {
				m.welcomeNotice = "open failed: " + err.Error()
			}
		}
		return true
	}
	if nav {
		switch key {
		case "up", "k":
			if m.sessionSel > 0 {
				m.sessionSel--
			}
		case "down", "j":
			if m.sessionSel < len(m.sessions)-1 {
				m.sessionSel++
			}
		case "pgup":
			m.sessionSel = max(0, m.sessionSel-10)
		case "pgdown":
			m.sessionSel = min(len(m.sessions)-1, m.sessionSel+10)
			if m.sessionSel < 0 {
				m.sessionSel = 0
			}
		case "home":
			m.sessionSel = 0
		case "end":
			if len(m.sessions) > 0 {
				m.sessionSel = len(m.sessions) - 1
			}
		}
		return true
	}
	return false
}

// handleChatEnter handles the enter key in chat mode, covering send, queue, and tool expand.
func (m *tuiModel) handleChatEnter(alt bool) (bool, bool, []tea.Cmd) {
	if alt {
		m.textarea.InsertString("\n")
		return true, false, nil
	}
	userText := strings.TrimSpace(m.textarea.Value())
	if userText == "" {
		if len(m.toolRows) > 0 &&
			(m.toolPanel.Focused || m.toolPanel.Selected >= 0) &&
			m.focus != focusComposer {
			if sel := m.toolPanel.Selected; sel >= 0 && sel < len(m.toolRows) {
				m.toolRows[sel].Expanded = !m.toolRows[sel].Expanded
				m.layout()
			}
			return true, false, nil
		}
		if len(m.pendingQueue) > 0 {
			m.sendNextQueued()
			cmds := m.takeQueuedSlashCmds()
			if m.waiting {
				return true, false, append(cmds, m.pollCmd())
			}
			if len(cmds) > 0 {
				return true, false, cmds
			}
		}
		return false, false, nil
	}
	fields := strings.Fields(userText)
	if len(fields) > 0 && strings.EqualFold(fields[0], "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(userText), fields[0]))
		if query == "" {
			m.appendInfo("usage: /search <query>")
			m.renderVP()
			return true, false, nil
		}
		userText = "search the web for: " + query
	}
	if strings.HasPrefix(userText, "/") {
		if m.handleSlash(userText) {
			m.textarea.Reset()
			m.renderVP()
			return true, false, m.takePendingSlashCmds()
		}
		if spec, ok := m.skillSlashSpec(userText); ok {
			if m.waiting {
				m.queueSkillTurn(spec)
				m.textarea.Reset()
				m.appendInfo(fmt.Sprintf("(queued: %s - %d pending, empty enter=force)", truncateStr(spec.display, 40), len(m.pendingQueue)))
				m.renderVP()
				return true, false, nil
			}
			m.startSkillAI(spec)
			return true, false, []tea.Cmd{m.pollCmd()}
		}
		m.appendInfo(fmt.Sprintf("unknown command %q (try /help)", fields[0]))
		m.renderVP()
		return true, false, nil
	}
	// Check for pending resume confirmation.
	if m.pendingResume != "" {
		m.handlePendingResumeInput(userText)
		m.textarea.Reset()
		m.renderVP()
		return true, false, nil
	}
	if m.waiting {
		m.queueTurn(userText, userText)
		m.textarea.Reset()
		m.appendInfo(fmt.Sprintf("(queued: %s - %d pending, empty enter=force)", truncateStr(userText, 40), len(m.pendingQueue)))
		m.renderVP()
		return true, false, nil
	}
	m.startAI(userText)
	return true, false, []tea.Cmd{m.pollCmd()}
}

// handleBlockActionKey runs the actions that operate on the selected chat
// block. Each declines (returns false) when it has nothing to act on, so the
// key falls through to normal routing instead of being swallowed half-broken.
//
// Every bare letter here is gated on scrollback focus: unconditionally bound,
// 'j'/'k'/'y'/'o' would make words like "just" and "you" untypable in the
// composer (INV-TUI-16).
func (m *tuiModel) handleBlockActionKey(key string) (bool, []tea.Cmd) {
	scrollback := m.focus == focusScrollback
	switch {
	case (key == "j" || key == "k") && scrollback:
		// Scroll the selected work group's bounded window first; then a
		// selected expanded thinking block whose content overflows its
		// window. Decline when neither applies so j/k keeps its normal
		// routing - bare letters act on blocks only under scrollback focus
		// (INV-TUI-16).
		if m.scrollSelectedWorkGroup(key == "j") {
			return true, nil
		}
		if m.selectedBlockID != "" {
			dir := 1
			if key == "k" {
				dir = -1
			}
			if m.adjustThinkingScroll(m.selectedBlockID, dir) {
				m.renderVP()
				return true, nil
			}
		}
		return false, nil
	case (key == "y" && scrollback) || key == "ctrl+y":
		// Yank the selected block to the system clipboard.
		cmd, ok := m.copySelectedBlock()
		if !ok {
			return false, nil
		}
		if cmd == nil {
			return true, nil
		}
		return true, []tea.Cmd{cmd}
	case key == "o" && scrollback:
		return m.openSelectedBlockOverlay(), nil
	case key == "ctrl+g":
		// Fleet detail overlay - full per-agent activity for this turn. A
		// no-op when no subagents ran, so the key stays inert, never
		// half-broken.
		return m.openFleetOverlay(), nil
	}
	return false, nil
}

func (m *tuiModel) handleChatKey(key string, alt bool) (bool, bool, []tea.Cmd) {
	// An armed quit is a moment, not a mode: anything other than the arming
	// key disarms it, or a ctrl+c minutes later exits with no warning.
	if key != "ctrl+c" {
		m.disarmQuit()
	}
	// Cancel and quit outrank every modal surface. ctrl+g opens the fleet
	// overlay mid-turn - the hint line says so - and while a modal consumed
	// every key, the one key that must always work could not stop a runaway
	// turn, and the documented unambiguous quit was not global either.
	if cmds, handled := m.handleModalEscapeKey(key); handled {
		return true, true, cmds
	}
	// Modal surfaces own the screen while open: every other key routes to them.
	if ok, skipText, cmds := m.routeModalKey(key); ok {
		return ok, skipText, cmds
	}
	if m.overlay != nil {
		return m.handleOverlayKey(key)
	}
	if handled := m.handleSidebarKey(key); handled {
		return true, true, nil
	}
	if handled := m.handleWorkflowsSidebarKey(key); handled {
		return true, true, m.takePendingWorkflowDialogCmd()
	}
	// Dashboard keys take priority when the dashboard panel is open. Only
	// non-typable keys are bound: a bare rune here is swallowed before it can
	// reach the composer, so "k"/"j" made words like "just" untypable and "r"
	// fired a real run resume on any word containing it. Resuming is /resume.
	// Scrollback focus only: the panel renders directly above the composer, so
	// letting it own the arrow keys while the composer has focus stole the
	// caret from anyone editing a multi-line draft (INV-TUI-16).
	if m.runDash != nil && m.runDash.isVisible() && m.focus == focusScrollback {
		switch key {
		case "up":
			m.runDash.cursorUp()
			m.layout()
			return true, true, nil
		case "down":
			m.runDash.cursorDown()
			m.layout()
			return true, true, nil
		}
	}
	if handled, skipViewport, cmds := m.handleComposerPopupKey(key); handled {
		return true, skipViewport, cmds
	}
	// Tab cycles focusable bubbles in history (not only pane toggle).
	if key == "tab" || key == "shift+tab" {
		if m.sidebarVisible() || m.workflowsSidebarVisible() {
			m.setFocus(m.nextTUIFocus(m.focus, key == "shift+tab"))
			return true, true, nil
		}
		if m.cycleChatFocus(key == "shift+tab") {
			return true, false, nil
		}
	}
	if key == "enter" || key == " " {
		if m.focus == focusScrollback && m.toggleSelectedBlock() {
			// skipViewport: bubbles binds space to PageDown, so letting it through
			// paged the transcript away from the block the user just expanded to read.
			return true, true, nil
		}
	}
	if handled, cmds := m.handleBlockActionKey(key); handled {
		return true, true, cmds
	}
	focus, consumed := routeFocusKey(m.focus, key)
	m.setFocus(focus)
	skipTextarea, skipViewport, cmds := m.handleChatControlKey(key, alt, consumed)
	// The transcript consumes keys only while it owns focus. bubbles' viewport
	// binds bare runes (u/d/b/f/space/k/j/h/l) and the arrow keys, and it has no
	// focus concept of its own: without this gate, typing in the composer
	// scrolled history and latched followOutput off for the rest of the session,
	// rendering later answers off-screen. routeFocusKey promotes
	// pgup/pgdown/home/end to focusScrollback above, so those still reach it.
	return skipTextarea, skipViewport || focus != focusScrollback, cmds
}

// handleChatToggleKey handles the mode/panel toggles, which share the shape
// "flip some view state, maybe relayout, never touch the composer".
func (m *tuiModel) handleChatToggleKey(key string) []tea.Cmd {
	switch key {
	case "ctrl+l":
		m.messages = nil
		m.blocks = nil
		m.msgOffset = 0
		m.viewport.SetContent("")
	case "ctrl+t":
		m.thinkingExpandDefault = !m.thinkingExpandDefault
		if m.selectedBlockID != "" {
			if block := m.blockByID(m.selectedBlockID); block != nil && block.Kind == ChatBlockThinking {
				block.Collapsed = !block.Collapsed
			}
		}
		m.renderVP()
	case "f2":
		// Select mode: hands the mouse back to the terminal so its own
		// selection works everywhere, including the composer. /select does
		// the same for terminals that eat function keys.
		//
		// NOT ctrl+e: bubbles binds that to line-end, and with home/end back
		// in the composer it was the last key standing between the user and
		// the end of their own line.
		//
		// NOT ctrl+s: that is XOFF. Where software flow control survives raw
		// mode (tmux, several terminals) it freezes output instead of
		// reaching the app, so the key that unblocks selection would be the
		// key that appears to hang the UI.
		//
		// NOT ctrl+m: 0x0D is carriage return - bubbletea aliases KeyCtrlM to
		// KeyEnter, so a "ctrl+m" branch is unreachable from a real terminal
		// and pressing the chord sends the draft instead.
		return []tea.Cmd{m.toggleSelectMode()}
	case "ctrl+r":
		if m.runDash != nil {
			m.runDash.trySubscribe()
			m.runDash.toggleOpen()
			m.layout()
		}
	}
	return nil
}

func (m *tuiModel) handleChatControlKey(key string, alt, skipTextarea bool) (bool, bool, []tea.Cmd) {
	var cmds []tea.Cmd
	// swallowViewport withholds a key from the transcript on top of the focus
	// gate in handleChatKey, for keys that must be inert in every focus.
	swallowViewport := false
	switch key {
	case "home", "shift+home":
		// Transcript top - but plain home belongs to the composer's line
		// editing while it has focus (it is the only line-start key the
		// composer has). shift+home reaches the transcript from anywhere.
		if key == "home" && m.focus == focusComposer {
			break
		}
		m.viewport.GotoTop()
		m.noteUserScrolledUp()
		m.renderVP()
		skipTextarea = true
		swallowViewport = true
	case "ctrl+c":
		return m.handleChatCancel()
	case "ctrl+q":
		// Unambiguous quit, since ctrl+c is cancel-then-quit.
		cmds = append(cmds, tea.Quit)
		skipTextarea = true
	case "ctrl+v":
		// mivia reads the clipboard itself (see clipboard_read.go). The
		// terminal's own paste (ctrl+shift+v) arrives as bracketed paste and
		// never reaches this branch.
		cmds = append(cmds, readClipboardCmd())
		skipTextarea = true
		swallowViewport = true
	case "esc":
		m.selectedBlockID = ""
		m.clearToolSelection()
		for i := range m.toolRows {
			m.toolRows[i].Expanded = false
		}
		m.layout()
		skipTextarea = true
	case "enter":
		return m.handleChatEnter(alt)
	case "ctrl+l", "ctrl+t", "f2", "ctrl+r":
		cmds = append(cmds, m.handleChatToggleKey(key)...)
		skipTextarea = true
	case "end", "shift+end":
		// Jump to latest when reading history (Phase D) - but plain end is
		// the composer's line-end key while a draft is being edited. With an
		// empty draft there is no line to move within, so end keeps its
		// reading meaning and the "↓ latest" affordance stays reachable
		// without a focus cycle.
		if key == "end" && m.focus == focusComposer && m.textarea.Value() != "" {
			break
		}
		m.jumpToLatest()
		m.renderVP()
		skipTextarea = true
		swallowViewport = true
	case "pgup", "up":
		if m.focus == focusScrollback {
			m.noteUserScrolledUp()
		}
	}
	return skipTextarea, swallowViewport, cmds
}

// handleWelcomeEnter processes Enter key press in welcome mode.
// Note: "exit"/"quit" are handled before this function is called (in tui_message.go).
func (m *tuiModel) handleWelcomeEnter(userText string) []tea.Cmd {
	if userText == "" {
		if len(m.sessions) == 0 {
			return nil
		}
		if err := m.openSelectedSession(); err != nil {
			m.welcomeNotice = "open failed: " + err.Error()
			return nil
		}
		m.welcomeNotice = ""
		m.textarea.Placeholder = composerPlaceholder
		return nil
	}
	m.welcomeNotice = ""
	m.beginNewSession()
	m.enterChatMode()
	m.textarea.Reset()
	m.textarea.Placeholder = composerPlaceholder
	fields := strings.Fields(userText)
	if len(fields) > 0 && strings.EqualFold(fields[0], "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(userText), fields[0]))
		if query == "" {
			m.appendInfo("usage: /search <query>")
			m.renderVP()
			return nil
		}
		userText = "search the web for: " + query
	}
	if strings.HasPrefix(userText, "/") {
		if m.handleSlash(userText) {
			m.renderVP()
			return m.takePendingSlashCmds()
		}
		if spec, ok := m.skillSlashSpec(userText); ok {
			m.startSkillAI(spec)
			return []tea.Cmd{m.pollCmd()}
		}
		m.appendInfo(fmt.Sprintf("unknown command %q (try /help)", fields[0]))
		m.renderVP()
		return nil
	}
	m.startAI(userText)
	return []tea.Cmd{m.pollCmd()}
}
