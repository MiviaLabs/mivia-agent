package cli

import (
	"strings"
	"time"

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
		m.logoFrame++
		return m, logoTickCmd()
	case tea.KeyMsg:
		key := msg.String()
		switch {
		case m.mode == modeChat:
			var c []tea.Cmd
			skipTextarea, skipViewport, c = m.handleChatKey(key, msg.Alt)
			if len(c) > 0 {
				return m, tea.Batch(append(cmds, c...)...)
			}
		case m.mode == modeWelcome && key == "enter":
			if msg.Alt {
				m.textarea.InsertString("\n")
				break
			}
			userText := strings.TrimSpace(m.textarea.Value())
			if userText == "exit" || userText == "quit" {
				return m, tea.Quit
			}
			cmds = append(cmds, m.handleWelcomeEnter(userText)...)
			skipTextarea = true
		case m.mode == modeWelcome:
			skipTextarea = m.handleWelcomeKey(key)
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
		if hit && zone.kind == hitTranscript && msg.Type == tea.MouseWheelUp {
			m.viewport.ViewUp()
			skipViewport = true
		}
		if hit && zone.kind == hitTranscript && msg.Type == tea.MouseWheelDown {
			m.viewport.ViewDown()
			skipViewport = true
		}
		if hit && zone.kind == hitTranscript && msg.Type == tea.MouseLeft {
			if zone.blockID != "" {
				m.selectedBlockID = zone.blockID
				m.setFocus(focusScrollback)
			}
		}
		if hit && zone.kind == hitComposer && msg.Type == tea.MouseLeft {
			m.setFocus(focusComposer)
		}
	}
	// Welcome and chat both use the composer; gating on modeChat only broke
	// typing on the welcome screen (↑↓ still worked via handleWelcomeKey).
	if !skipTextarea && (m.mode == modeChat || m.mode == modeWelcome) {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		if cmd != nil {
			// Don't crash if the poll timer fires while model has no bridge.
			cmds = append(cmds, cmd)
		}
	}
	// Viewport updates: skip only when mouse wheel already scrolled it.
	if m.mode == modeChat && !skipViewport {
		m.viewport, _ = m.viewport.Update(msg)
	}
	if m.mode == modeChat {
		m.mu.Lock()
		stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt := m.bridge.Drain()
		m.mu.Unlock()
		m.stepDetail = stepDetail
		if !stepDetailAt.IsZero() {
			m.stepDetailAt = stepDetailAt
		}
		if len(tools) > 0 {
			m.applyToolEvents(tools)
			if m.waiting && !m.stalledWarning {
				m.layout()
				m.renderStreamVP()
			}
			cmds = append(cmds, m.pollCmd())
		}
		if stream != "" || done || doneErr != nil {
			if stream != "" {
				m.streamBuf.WriteString(stream)
			}
			if done || doneErr != nil {
				cmds = append(cmds, m.finishStream(doneErr)...)
			}
			if !done {
				m.renderStreamVP()
			}
			cmds = append(cmds, m.pollCmd())
		}
		if thinking != "" || done || doneErr != nil {
			if thinking != "" {
				m.thinkingBuf.WriteString(thinking)
			}
			if !done {
				m.renderStreamVP()
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) handleSlash(cmd string) bool {
	return handleSlashImpl(m, cmd)
}
