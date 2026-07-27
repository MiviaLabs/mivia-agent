package cli

import (
	"context"
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
				m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
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

// handleChatCancel handles the ctrl+c key for cancelling the current turn or quitting.
// Cancel preserves the partial story (interim, status, tools) and appends a
// cancelled footer — web-like stop, not wipe (Phase E).
//
// Stage 1 (waiting): cancel the current turn, show cancelled state.
// Stage 2 (cancelling, agent goroutine still unwinding): set quitRequested.
//
//	The agent goroutine's bridge.Finish eventually triggers the poll loop,
//	which sends tea.Quit only after the worker has finished, ensuring
//	SaveLast runs before the process exits.
//
// Stage 3 (fully idle): quit immediately (normal flow).
func (m *tuiModel) handleChatCancel() (bool, bool, []tea.Cmd) {
	if m.waiting {
		// Stage 1: first Ctrl+C — cancel the turn.
		m.mu.Lock()
		if m.cancel != nil {
			m.cancel()
		}
		br := m.bridge
		if br != nil {
			// Finish (not Close) so any final drain is coherent; fence drops later events.
			br.Finish(context.Canceled)
		}
		m.mu.Unlock()
		if br != nil {
			m.updateFromDrain(br.Drain())
		}
		cmds := m.finishStream(context.Canceled)
		m.cancelling = true
		m.textarea.Reset()
		return true, false, cmds
	}
	if m.cancelling {
		// Stage 2: second Ctrl+C while agent goroutine still unwinding.
		// Don't quit yet — the deferred SaveLast would race with the
		// goroutine. Set quitRequested; the poll loop will send tea.Quit
		// when the bridge signals the goroutine is truly done.
		m.quitRequested = true
		m.appendInfo("(quitting after cancel completes…)")
		m.renderVP()
		return true, false, nil
	}
	// Stage 3: fully idle — quit immediately.
	return false, false, []tea.Cmd{tea.Quit}
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
			if m.waiting {
				return true, false, []tea.Cmd{m.pollCmd()}
			}
		}
		return false, false, nil
	}
	if strings.HasPrefix(userText, "/search") {
		query := strings.TrimSpace(userText[7:])
		if query == "" {
			m.appendInfo("usage: /search <query>")
			m.renderVP()
			return true, false, nil
		}
		userText = "search the web for: " + query
	}
	if strings.HasPrefix(userText, "/") {
		if m.handleSlash(userText) {
			m.renderVP()
			return true, false, nil
		}
	}
	if m.waiting {
		m.pendingQueue = append(m.pendingQueue, userText)
		m.textarea.Reset()
		m.appendInfo(fmt.Sprintf("(queued: %s — %d pending, empty enter=force)", truncateStr(userText, 40), len(m.pendingQueue)))
		m.renderVP()
		return true, false, nil
	}
	m.startAI(userText)
	return true, false, []tea.Cmd{m.pollCmd()}
}

// handleChatKey handles key events in chat mode.
// Returns (skipTextarea, skipViewport, cmds).
func (m *tuiModel) handleChatKey(key string, alt bool) (bool, bool, []tea.Cmd) {
	var cmds []tea.Cmd
	skipTextarea := false

	// Focus routing: enter/space from scrollback expands selected block.
	if key == "enter" || key == " " {
		if m.focus == focusScrollback && m.toggleSelectedBlock() {
			return true, false, nil
		}
	}
	focus, consumed := routeFocusKey(m.focus, key)
	m.setFocus(focus)
	if consumed {
		skipTextarea = true
	}

	switch key {
	case "ctrl+c":
		return m.handleChatCancel()
	case "esc":
		m.clearToolSelection()
		for i := range m.toolRows {
			m.toolRows[i].Expanded = false
		}
		m.layout()
		skipTextarea = true
	case "ctrl+d":
		m.mu.Lock()
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Unlock()
		return false, false, []tea.Cmd{tea.Quit}
	case "enter":
		return m.handleChatEnter(alt)
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
		skipTextarea = true
	case "ctrl+m":
		m.mouseEnabled = !m.mouseEnabled
		skipTextarea = true
		if m.mouseEnabled {
			cmds = append(cmds, tea.EnableMouseCellMotion)
		} else {
			cmds = append(cmds, tea.DisableMouse)
		}
	case "end":
		// Jump to latest when reading history (Phase D).
		if m.focus == focusScrollback || !m.followOutput {
			m.jumpToLatest()
			skipTextarea = true
		}
	case "pgup", "up":
		if m.focus == focusScrollback {
			m.noteUserScrolledUp()
		}
	}
	return skipTextarea, false, cmds
}

// handleWelcomeEnter processes Enter key press in welcome mode.
func (m *tuiModel) handleWelcomeEnter(userText string) []tea.Cmd {
	if userText == "" {
		if len(m.sessions) == 0 {
			return nil
		}
		if err := m.openSelectedSession(); err != nil {
			return nil
		}
		m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
		return nil
	}
	if userText == "exit" || userText == "quit" {
		return []tea.Cmd{tea.Quit}
	}
	m.beginNewSession()
	m.enterChatMode()
	m.textarea.Reset()
	m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
	if strings.HasPrefix(userText, "/search") {
		query := strings.TrimSpace(userText[7:])
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
			return nil
		}
	}
	m.startAI(userText)
	return []tea.Cmd{m.pollCmd()}
}
