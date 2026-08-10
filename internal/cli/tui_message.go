package cli

import (
	"strings"

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
		// Adapter heartbeat only - do not drain bridge here (tuiTickMsg owns it).
		if m.uiAdapter != nil {
			cmds = append(cmds, m.uiAdapter.PollCmd())
		}
		// Runs started or advanced in other terminals reach the /workflows
		// sidebar here. refreshWorkflowsSidebar throttles internally and
		// returns a tea.Cmd, so this tick starts at most one ledger read per
		// interval and that read runs off the update goroutine.
		if cmd := m.refreshWorkflowsSidebar(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case workflowsSidebarRefreshMsg:
		// The ledger read ran off the update goroutine. Apply the rows only
		// while the sidebar is still open (it may have been closed while the
		// read was in flight); a failed read keeps the previous rows. The
		// adapter poll chain is re-issued by the event or tick that triggered
		// the refresh, so nothing more is needed here.
		if m.workflowsSidebar != nil && msg.err == nil {
			sidebar := m.workflowsSidebar
			sidebar.rows = msg.rows
			sidebar.dirty = false
			sidebar.move(msg.rows, 0)
			m.renderVP()
		}
		return m, nil
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
		m.clampModalState()
		if m.focus == focusSidebar && !m.sidebarVisible() {
			m.setFocus(focusComposer)
		}
		if m.focus == focusWorkflowsSidebar && !m.workflowsSidebarVisible() {
			m.setFocus(focusComposer)
		}
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
	case copyResultMsg:
		m.noteCopyResult(msg)
		return m, nil
	case pasteTextMsg:
		m.disarmQuit()
		if m.modalOpen() {
			return m, nil
		}
		m.applyPastedText(msg.text)
		return m, nil
	case pasteFailedMsg:
		m.disarmQuit()
		if m.modalOpen() {
			return m, nil
		}
		m.notePasteFailure(msg.err)
		return m, nil
	case worktreeCreatedMsg:
		m.applyWorktreeCreated(msg)
		if m.restartWorkspace != "" {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyMsg:
		if msg.Paste {
			m.disarmQuit()
			if m.modalOpen() {
				return m, nil
			}
			// Bracketed paste: one atomic insert, never routed as keys.
			skipTextarea, skipViewport = m.routePastedInput()
			break
		}
		key := msg.String()
		switch {
		case m.mode == modeChat:
			var c []tea.Cmd
			skipTextarea, skipViewport, c = m.handleChatKey(key, msg.Alt)
			if m.restartWorkspace != "" {
				return m, tea.Quit
			}
			if len(c) > 0 {
				return m, tea.Batch(append(cmds, c...)...)
			}
		case m.mode == modeWelcome && (key == "ctrl+c" || key == "ctrl+q"):
			// The welcome screen has no draft worth protecting and no
			// selection to copy, so ctrl+c stays a plain quit here; ctrl+q
			// quits from every screen.
			return m, tea.Quit
		case m.mode == modeWelcome:
			if consumed, skipView, c := m.handleSuggestKey(key); consumed {
				skipTextarea, skipViewport = true, skipView
				if len(c) > 0 {
					return m, tea.Batch(append(cmds, c...)...)
				}
				break
			}
			if key == "enter" {
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
				break
			}
			skipTextarea = m.handleWelcomeKey(key)
		}
	case tea.MouseMsg:
		// Mouse input disarms a pending quit for the same reason a key does:
		// an arm that survives clicking a message turns the next ctrl+c into
		// an exit when the user meant "copy that".
		m.disarmQuit()
		if m.handleModalMouse(msg) {
			return m, nil
		}
		if msg.Type == tea.MouseRight {
			if zone, hit := m.hitMap.hit(msg.Y); hit && zone.kind == hitTranscript && zone.blockID != "" {
				if cmd, ok := m.copyBlockByID(zone.blockID); ok {
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}
			}
		}
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
	if m.mode == modeChat || m.mode == modeWelcome {
		if !m.modalOpen() && m.focus == focusComposer {
			m.syncSuggest()
		}
	}
	if m.mode == modeChat {
		// Foot drain: catch bridge updates between ticks (key/mouse path).
		cmds = append(cmds, m.drainBridgeAndMaybeFinish()...)
	}
	if m.restartWorkspace != "" {
		return m, tea.Quit
	}
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) modalOpen() bool {
	return m.overlay != nil || m.modelDlg != nil || m.agentDlg != nil || m.effortDlg != nil || m.worktreeDlg != nil
}

func (m *tuiModel) clampModalState() {
	if m.overlay != nil {
		_, _ = m.overlay.ViewAt(max(1, m.width), max(1, m.height))
	}
	if m.modelDlg != nil {
		layout := m.modelDlg.layout(max(1, m.width), max(1, m.height))
		m.modelDlg.clampScroll(layout.pageH)
	}
	if m.agentDlg != nil {
		layout := m.agentDlg.layout(max(1, m.width), max(1, m.height))
		m.agentDlg.clampScroll(layout.pageH)
	}
	if m.effortDlg != nil {
		layout := m.effortDlg.layout(max(1, m.width), max(1, m.height))
		m.effortDlg.clampScroll(layout.pageH)
	}
	if m.worktreeDlg != nil {
		layout := m.worktreeDlg.layout(max(1, m.width), max(1, m.height))
		m.worktreeDlg.clampScroll(layout.pageH)
	}
}

// drainBridgeAndMaybeFinish pulls coalesced stream/tool/thinking/done from the
// bridge into model state. This is the live TUI content path.
// When quitRequested is true and the bridge signals the agent goroutine has
// finished, it also sends tea.Quit so SaveLast runs before process exit.
//
// Concurrency: captures m.bridge under the mutex so startAI (which swaps the
// bridge under the same lock) cannot cause a data race between the nil check
// and the Drain call. The captured bridge is safe to drain even after being
// replaced - the old bridge's Close() just stops accepting new writes, and
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
		// Cancel unwind finished without quitRequested - clear cancelling so
		// the next Ctrl+C is a normal idle quit (stage 3), not a stuck stage 2.
		if m.cancelling {
			m.cancelling = false
		}
		return cmds
	}
	return nil
}

func (m *tuiModel) handleSlash(cmd string) bool {
	return handleSlashImpl(m, cmd)
}
