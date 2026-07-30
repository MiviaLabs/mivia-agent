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
	case uiEventMsg:
		// EventBus side channel. Content/tools/finish are owned by the bridge
		// drain path (tuiTickMsg). Only apply non-duplicative kinds here.
		// Attributed subagent events also pass while !waiting: they can land
		// before the first drain flips it, and a dropped start leaves the
		// fleet box empty for the rest of the turn.
		if m.mode == modeChat && (m.waiting || msg.event.AgentTask != "") {
			cmds = append(cmds, m.applyEvent(msg.event)...)
		}
		if m.uiAdapter != nil {
			cmds = append(cmds, m.uiAdapter.PollCmd())
		}
		return m, tea.Batch(cmds...)
	case uiTickMsg:
		// Adapter heartbeat only — do not drain bridge here (tuiTickMsg owns it).
		if m.uiAdapter != nil {
			cmds = append(cmds, m.uiAdapter.PollCmd())
		}
		return m, tea.Batch(cmds...)
	case tuiTickMsg:
		if m.mode == modeChat && msg.bridge == m.bridge {
			cmds = append(cmds, m.drainBridgeAndMaybeFinish()...)
		}
		// Always re-queue pollCmd (self-perpetuating tick chain).
		cmds = append(cmds, m.pollCmd())
		return m, tea.Batch(cmds...)
	case agentQuitReadyMsg:
		// Post-cancel quit waiter finished (worker done or timeout).
		if m.quitRequested || m.cancelling {
			m.quitRequested = false
			m.cancelling = false
			m.agentDone = true
			return m, tea.Quit
		}
		return m, nil
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
	case periodicSaveMsg:
		// Periodic auto-save during long conversations.
		// Only save when in chat mode with actual conversation content.
		if m.mode == modeChat {
			m.session.SaveAfterTurn()
		}
		// Re-queue the periodic save regardless.
		return m, periodicSaveCmd()
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
		case m.mode == modeWelcome && key == "ctrl+c":
			return m, tea.Quit
		case m.mode == modeWelcome:
			skipTextarea = m.handleWelcomeKey(key)
		}
	case tea.MouseMsg:
		if m.handleMouseMsg(msg, &skipViewport) {
			break
		}
	}
	// Welcome and chat both use the composer; gating on modeChat only broke
	// typing on the welcome screen (↑↓ still worked via handleWelcomeKey).
	if !skipTextarea && (m.mode == modeChat || m.mode == modeWelcome) {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// Viewport updates: skip only when mouse wheel already scrolled it.
	if m.mode == modeChat && !skipViewport {
		oldOff := m.viewport.YOffset
		m.viewport, _ = m.viewport.Update(msg)
		// If the viewport's own fallback handling scrolled (wheel over
		// non-transcript zone or missed zone), mark user as scrolled up
		// so followOutput does not yank back to bottom on next stream tick.
		if m.viewport.YOffset != oldOff && !m.viewport.AtBottom() {
			m.noteUserScrolledUp()
		}
	}
	if m.mode == modeChat {
		// Foot drain: catch bridge updates between ticks (key/mouse path).
		cmds = append(cmds, m.drainBridgeAndMaybeFinish()...)
	}
	return m, tea.Batch(cmds...)
}

// drainBridgeAndMaybeFinish pulls coalesced stream/tool/thinking/done from the
// bridge into model state. This is the live TUI content path.
// When quitRequested is true and the bridge signals the agent goroutine has
// finished, it also sends tea.Quit so SaveLast runs before process exit.
//
// Concurrency: captures m.bridge under the mutex so startAI (which swaps the
// bridge under the same lock) cannot cause a data race between the nil check
// and the Drain call. The captured bridge is safe to drain even after being
// replaced — the old bridge's Close() just stops accepting new writes, and
// Drain() returns whatever state remains under its own internal lock.
func (m *tuiModel) drainBridgeAndMaybeFinish() []tea.Cmd {
	m.mu.Lock()
	bridge := m.bridge
	m.mu.Unlock()
	if bridge == nil {
		return nil
	}
	d := bridge.Drain()
	m.updateFromDrain(d)
	if d.Done || d.DoneErr != nil {
		// Worker signaled completion (or stage-1 Finish). Remember even when
		// finishStream is a no-op (waiting already false after cancel).
		m.agentDone = true
		cmds := m.finishStream(d.DoneErr)
		if m.quitRequested {
			// Agent goroutine is done, bridge is drained, SaveLast will
			// run through the runTUI defer. Send the quit now.
			m.cancelling = false
			m.quitRequested = false
			return append(cmds, tea.Quit)
		}
		// Cancel unwind finished without quitRequested — clear cancelling so
		// the next Ctrl+C is a normal idle quit (stage 3), not a stuck stage 2.
		if m.cancelling {
			m.cancelling = false
		}
		return cmds
	}
	return nil
}

// handleMouseMsg updates selection/scroll from mouse input.
// Returns true when the welcome-mode branch fully handled the event.
func (m *tuiModel) handleMouseMsg(msg tea.MouseMsg, skipViewport *bool) bool {
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
		return true
	}
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

// handleTranscriptBlockClick selects a chat block (or work: group). A second
// click on the same ID within 400ms activates (toggle collapse) — same as Enter.
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

func (m *tuiModel) handleSlash(cmd string) bool {
	return handleSlashImpl(m, cmd)
}
