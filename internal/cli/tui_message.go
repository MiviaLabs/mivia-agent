package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	return updateMessageImpl(m, msg)
}

var updateMessageImpl = func(m *tuiModel, msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	skipTextarea := false
	skipViewport := false
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.hitMap.invalidate()
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		if m.mode == modeChat {
			m.renderVP()
		}

	case logoTickMsg:
		if m.mode == modeWelcome || m.waiting {
			m.logoFrame++
			return m, logoTickCmd()
		}

	case tea.KeyMsg:
		if m.mode == modeChat && (msg.String() == "enter" || msg.String() == " ") &&
			m.focus == focusScrollback && m.toggleSelectedBlock() {
			skipTextarea = true
			break
		}
		if m.mode == modeChat {
			focus, consumed := routeFocusKey(m.focus, msg.String(), len(m.toolRows) > 0)
			m.setFocus(focus)
			if consumed {
				skipTextarea = true
			}
		}
		if msg.String() == "ctrl+c" {
			if m.waiting {
				m.mu.Lock()
				if m.cancel != nil {
					m.cancel()
				}
				m.bridge.Close()
				m.mu.Unlock()
				m.waiting = false
				m.toolRows = nil
				m.clearToolSelection()
				m.toolPanel = toolPanelState{Selected: -1}
				m.streamBuf.Reset()
				m.layout()
				m.renderVP()
				m.textarea.Reset()
				m.appendInfo("(cancelled — type a new message)")
			} else {
				return m, tea.Quit
			}
			break
		}

		if msg.String() == "esc" {
			if m.mode == modeWelcome {
				skipTextarea = true
				break
			}
			m.clearToolSelection()
			for i := range m.toolRows {
				m.toolRows[i].Expanded = false
			}
			m.layout()
			skipTextarea = true
			break
		}

		if m.mode == modeWelcome {
			composerEmpty := strings.TrimSpace(m.textarea.Value()) == ""
			key := msg.String()
			nav := false
			switch key {
			case "up":
				nav = true
			case "down":
				nav = true
			case "k", "j":
				nav = composerEmpty
			case "pgup", "pgdown", "home", "end":
				nav = true
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
				skipTextarea = true
			}
		}

		switch msg.String() {
		case "ctrl+d":
			m.mu.Lock()
			if m.cancel != nil {
				m.cancel()
			}
			m.mu.Unlock()
			return m, tea.Quit
		case "enter":
			if msg.Alt {
				m.textarea.InsertString("\n")
				skipTextarea = true
				break
			}
			userText := strings.TrimSpace(m.textarea.Value())

			if m.mode == modeWelcome {
				skipTextarea = true
				if userText == "exit" || userText == "quit" {
					return m, tea.Quit
				}
				if userText != "" {
					m.beginNewSession()
					m.enterChatMode()
					m.textarea.Reset()
					m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
					if strings.HasPrefix(userText, "/search") {
						query := strings.TrimSpace(userText[7:])
						if query == "" {
							m.appendInfo("usage: /search <query>")
							m.renderVP()
							return m, nil
						}
						userText = "search the web for: " + query
					}
					if strings.HasPrefix(userText, "/") {
						if m.handleSlash(userText) {
							m.renderVP()
							return m, nil
						}
					}
					m.startAI(userText)
					return m, tea.Batch(m.pollCmd(), logoTickCmd())
				}
				if len(m.sessions) == 0 {
					break
				}
				if err := m.openSelectedSession(); err != nil {
					break
				}
				m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
				return m, nil
			}

			if userText == "" {
				if m.mode == modeChat && len(m.toolRows) > 0 &&
					(m.toolPanel.Focused || m.toolPanel.Selected >= 0) &&
					m.toolPanel.Selected >= 0 && m.toolPanel.Selected < len(m.toolRows) {
					m.toolRows[m.toolPanel.Selected].Expanded = !m.toolRows[m.toolPanel.Selected].Expanded
					m.layout()
					skipTextarea = true
					break
				}
				if m.waiting && len(m.pendingQueue) > 0 {
					m.forceSendQueued()
					return m, tea.Batch(m.pollCmd(), logoTickCmd())
				}
				skipTextarea = true
				break
			}
			if userText == "exit" || userText == "quit" {
				return m, tea.Quit
			}

			if strings.HasPrefix(userText, "/search") {
				query := strings.TrimSpace(userText[7:])
				if query == "" {
					m.appendInfo("usage: /search <query>")
					m.renderVP()
					m.textarea.Reset()
					skipTextarea = true
					break
				}
				userText = "search the web for: " + query
			}

			if !m.waiting && strings.HasPrefix(userText, "/") {
				if m.handleSlash(userText) {
					m.renderVP()
					m.textarea.Reset()
					skipTextarea = true
					break
				}
			}

			if m.waiting {
				const maxPendingQueue = 64
				if len(m.pendingQueue) >= maxPendingQueue {
					m.appendInfo("(queue full: 64 messages; send or cancel the active turn first)")
					m.renderVP()
					return m, tea.Batch(cmds...)
				}
				m.pendingQueue = append(m.pendingQueue, userText)
				m.textarea.Reset()
				m.appendInfo(fmt.Sprintf("(queued: %s — %d pending, empty enter=force)", truncateStr(userText, 40), len(m.pendingQueue)))
				m.renderVP()
				return m, tea.Batch(cmds...)
			}
			m.startAI(userText)
			return m, tea.Batch(m.pollCmd(), logoTickCmd())

		case "ctrl+l":
			m.messages = nil
			m.blocks = nil
			m.msgOffset = 0
			m.viewport.SetContent("")

		case "tab":
			if m.mode == modeChat && m.focus == focusTools {
				m.toolPanel.selectNext(+1, toolMaxVisibleRows)
				skipTextarea = true
			}
		case "shift+tab":
			if m.mode == modeChat && m.focus == focusTools {
				m.toolPanel.selectNext(-1, toolMaxVisibleRows)
				skipTextarea = true
			}
		case "up":
			if m.mode == modeChat && len(m.toolRows) > 0 &&
				m.focus == focusTools {
				m.toolPanel.selectNext(-1, toolMaxVisibleRows)
				skipTextarea = true
			}
		case "down":
			if m.mode == modeChat && len(m.toolRows) > 0 &&
				m.focus == focusTools {
				m.toolPanel.selectNext(+1, toolMaxVisibleRows)
				skipTextarea = true
			}

		case " ":
			if consumeToolNavKey(m.toolPanel.Selected, " ", strings.TrimSpace(m.textarea.Value()) == "") &&
				m.toolPanel.Selected < len(m.toolRows) {
				m.toolRows[m.toolPanel.Selected].Expanded = !m.toolRows[m.toolPanel.Selected].Expanded
				m.layout()
				skipTextarea = true
			}

		case "e":
			if consumeToolNavKey(m.toolPanel.Selected, "e", strings.TrimSpace(m.textarea.Value()) == "") {
				for i := range m.toolRows {
					m.toolRows[i].Expanded = true
				}
				m.layout()
				skipTextarea = true
			}
		case "E":
			if consumeToolNavKey(m.toolPanel.Selected, "E", strings.TrimSpace(m.textarea.Value()) == "") {
				for i := range m.toolRows {
					m.toolRows[i].Expanded = false
				}
				m.layout()
				skipTextarea = true
			}
		case "G":
			if consumeToolNavKey(m.toolPanel.Selected, "G", strings.TrimSpace(m.textarea.Value()) == "") {
				m.viewport.GotoBottom()
				skipTextarea = true
			}
		}

	case tea.MouseMsg:
		if m.mode == modeWelcome {
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
							m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
						}
						m.lastClickIdx = -1
					} else {
						m.sessionSel = idx
						m.lastClickIdx = idx
						m.lastClickAt = now
					}
				}
			}
			break
		}
		zone, hit := m.hitMap.hit(msg.Y)
		// Mouse wheel over thinking blocks scrolls their content.
		if hit && zone.blockID != "" && (msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown) {
			dir := 1
			if msg.Type == tea.MouseWheelUp {
				dir = -1
			}
			if m.adjustThinkingScroll(zone.blockID, dir) {
				m.renderVP()
				skipViewport = true
				break
			}
		}
		if hit && zone.kind == hitTools {
			switch msg.Type {
			case tea.MouseWheelUp:
				m.setFocus(focusTools)
				m.toolPanel.scrollWindow(-1, toolMaxVisibleRows)
				skipViewport = true
			case tea.MouseWheelDown:
				m.setFocus(focusTools)
				m.toolPanel.scrollWindow(+1, toolMaxVisibleRows)
				skipViewport = true
			case tea.MouseLeft:
				idx := m.toolPanel.toolIndexAtY(msg.Y)
				if idx >= 0 {
					m.setFocus(focusTools)
					if idx == m.toolPanel.Selected {
						if idx < len(m.toolRows) {
							m.toolRows[idx].Expanded = !m.toolRows[idx].Expanded
						}
					} else {
						m.toolPanel.Selected = idx
						m.toolPanel.Focused = true
						m.toolPanel.Scroll = clampToolScroll(
							m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
						)
					}
					m.layout()
				}
				skipViewport = true
			}
			break
		}
		if msg.Type == tea.MouseLeft && (!hit || zone.kind == hitTranscript) {
			if hit && zone.kind == hitTranscript && zone.blockID != "" {
				m.selectedBlockID = zone.blockID
				m.setFocus(focusScrollback)
				skipViewport = true
				break
			}
			if !m.viewport.AtBottom() {
				m.viewport.GotoBottom()
			}
		}

	case tuiTickMsg:
		if !m.waiting {
			return m, nil
		}
		m.mu.Lock()
		currentBridge := m.bridge
		m.mu.Unlock()
		if msg.bridge != nil && msg.bridge != currentBridge {
			return m, nil
		}
		stream, toolEvts, done, doneErr, thinking, _, _ := m.bridge.Drain()
		m.applyToolEvents(toolEvts)
		needsLayout := len(toolEvts) > 0
		if stream != "" {
			m.streamBuf.WriteString(stream)
		}
		if thinking != "" {
			m.thinkingBuf.WriteString(thinking)
		}
		if needsLayout {
			m.layout()
		}
		m.renderStreamVP()
		if done {
			cmdsFromFinish := m.finishStream(doneErr)
			cmds = append(cmds, cmdsFromFinish...)
			if !m.waiting {
				return m, tea.Batch(cmds...)
			}
			return m, tea.Batch(append(cmds, m.pollCmd(), logoTickCmd())...)
		}
		return m, m.pollCmd()

	case spinner.TickMsg:
		if m.waiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	if km, ok := msg.(tea.KeyMsg); ok && m.mode == modeChat {
		k := km.String()
		if k == "pgup" || k == "pgdown" || k == "home" || k == "end" {
			skipTextarea = true
		}
		toolsOwnArrows := len(m.toolRows) > 0 && m.focus == focusTools
		if m.focus == focusScrollback && (k == "up" || k == "down") {
			skipTextarea = true
			if toolsOwnArrows {
				skipViewport = true
			}
		}
		if toolsOwnArrows && (k == "up" || k == "down") {
			skipViewport = true
		}
	}

	if !skipTextarea {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(v)
		cmds = append(cmds, vpCmd)
	case tea.MouseMsg:
		if m.mode == modeWelcome || skipViewport {
			break
		}
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(v)
		m.hitMap.invalidate()
		cmds = append(cmds, vpCmd)
		if tryLoadHistoryNearTop(m.msgOffset, m.viewport.YOffset) {
			m.loadMoreMessages()
		}
	case tea.KeyMsg:
		if m.mode == modeWelcome || skipViewport {
			break
		}
		k := v.String()
		switch {
		case k == "home":
			m.viewport.GotoTop()
			for i := 0; i < 3 && m.msgOffset > 0; i++ {
				before := m.msgOffset
				m.loadMoreMessages()
				if m.msgOffset == before {
					break
				}
				m.viewport.GotoTop()
			}
		case k == "end":
			m.viewport.GotoBottom()
		case k == "up" || k == "down" || k == "pgup" || k == "pgdown":
			if m.focus == focusScrollback || k == "pgup" || k == "pgdown" {
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(v)
				m.hitMap.invalidate()
				cmds = append(cmds, vpCmd)
				if k == "pgup" || k == "up" {
					if tryLoadHistoryNearTop(m.msgOffset, m.viewport.YOffset) {
						m.loadMoreMessages()
					}
				}
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) handleSlash(cmd string) bool {
	return handleSlashImpl(m, cmd)
}
