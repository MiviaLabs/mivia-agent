package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

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
// Stage 2 (cancelling, agent still unwinding): set quitRequested; quit when
// agentDone (worker Finish already drained) or when the next Done arrives.
// Stage 2b (quitRequested already): force Quit — never strand on hung tools.
// Stage 3 (fully idle): quit immediately.
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
			// Stage-1 Finish is drained here; mark agentDone only when the
			// *worker* later Finishes again, or when we observe Done with
			// quitRequested. Do not set agentDone from this synthetic Finish
			// alone — worker may still be running tools.
			m.updateFromDrain(br.Drain())
		}
		cmds := m.finishStream(context.Canceled)
		m.cancelling = true
		// Synthetic stage-1 Finish was drained above and cleared bridge.done.
		// agentDone stays false until worker Finish is drained (or force quit).
		m.textarea.Reset()
		return true, false, cmds
	}
	if m.quitRequested {
		// Stage 2b: user already asked to quit once after cancel; force exit.
		// Hung tools / missed Done must not pin the TUI forever.
		m.cancelling = false
		m.quitRequested = false
		m.appendInfo("(force quit)")
		m.renderVP()
		return true, false, []tea.Cmd{tea.Quit}
	}
	if m.cancelling {
		// Stage 2: second Ctrl+C while agent goroutine may still be unwinding.
		if m.agentDone {
			// Worker Finish already observed (possibly before quitRequested).
			// Quit immediately — waiting for another Done strands the session.
			m.cancelling = false
			m.quitRequested = false
			return true, false, []tea.Cmd{tea.Quit}
		}
		m.quitRequested = true
		m.appendInfo("(quitting after cancel completes…)")
		m.renderVP()
		// Backup: wait for workerWG with timeout, then quit even if Done was missed.
		return true, false, []tea.Cmd{m.waitAgentThenQuitCmd()}
	}
	// Stage 3: fully idle — quit immediately.
	return false, false, []tea.Cmd{tea.Quit}
}

// waitAgentThenQuitCmd waits for the agent worker to finish (or timeout), then
// emits tea.Quit if quitRequested is still set. Prevents permanent strand when
// bridge Done was drained before quitRequested without agentDone.
func (m *tuiModel) waitAgentThenQuitCmd() tea.Cmd {
	return func() tea.Msg {
		done := make(chan struct{})
		go func() {
			m.workerWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			// Hung tools after cancel: do not block exit forever.
		}
		return agentQuitReadyMsg{}
	}
}

// agentQuitReadyMsg is delivered when the post-cancel quit waiter finishes.
type agentQuitReadyMsg struct{}

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
	// Check for pending resume confirmation.
	if m.pendingResume != "" {
		m.handlePendingResumeInput(userText)
		m.textarea.Reset()
		m.renderVP()
		return true, false, nil
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
func (m *tuiModel) handleChatKey(key string, alt bool) (bool, bool, []tea.Cmd) {
	// Dashboard keys take priority when the dashboard panel is open. Only
	// non-typable keys are bound: a bare rune here is swallowed before it can
	// reach the composer, so "k"/"j" made words like "just" untypable and "r"
	// fired a real run resume on any word containing it. Resuming is /resume.
	if m.runDash != nil && m.runDash.isOpen() {
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
	// Tab cycles focusable bubbles in history (not only pane toggle).
	if key == "tab" || key == "shift+tab" {
		if m.cycleChatFocus(key == "shift+tab") {
			return true, false, nil
		}
	}
	if key == "enter" || key == " " {
		if m.focus == focusScrollback && m.toggleSelectedBlock() {
			return true, false, nil
		}
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

func (m *tuiModel) handleChatControlKey(key string, alt, skipTextarea bool) (bool, bool, []tea.Cmd) {
	var cmds []tea.Cmd
	switch key {
	case "ctrl+c":
		return m.handleChatCancel()
	case "esc":
		m.selectedBlockID = ""
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
	case "ctrl+r":
		if m.runDash != nil {
			m.runDash.trySubscribe()
			m.runDash.toggleOpen()
			m.layout()
		}
		skipTextarea = true
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
// Note: "exit"/"quit" are handled before this function is called (in tui_message.go).
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
