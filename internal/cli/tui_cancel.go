package cli

// The ctrl+c cancel/quit state machine - running-turn and idle.
//
// ctrl+c keeps its terminal meaning while a turn is running: it cancels, and
// nothing else (handleChatCancel). At rest it used to quit on the first press,
// which made two everyday actions destructive - selecting a message and then
// pressing ctrl+c out of habit exited the app instead of copying (the copy
// guard demanded scrollback focus), and a half-typed question was thrown away
// by a single keystroke with no warning.
//
// Idle precedence, most-recoverable first:
//
//	1. a selected message  → copy it, and consume the selection
//	2. a non-empty draft   → clear it, and arm the quit
//	3. otherwise           → arm the quit; a second press within the window quits
//
// The arm is a moment, not a mode: it expires, and any other input disarms it.
// Otherwise a ctrl+c minutes later would exit with no warning at all.

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// takePendingSlashCmds drains the terminal command a slash handler produced.
// The state change already happened in the handler; this only carries the
// escape sequence out to bubbletea. Every caller of handleSlash must drain,
// or the mode and the terminal disagree.
func (m *tuiModel) takePendingSlashCmds() []tea.Cmd {
	if m.pendingSelectCmd == nil {
		return nil
	}
	cmd := m.pendingSelectCmd
	m.pendingSelectCmd = nil
	return []tea.Cmd{cmd}
}

// quitArmWindow bounds how long an armed quit stays armed.
const quitArmWindow = 3 * time.Second

// quitArmNotice is the on-screen prompt for an armed quit.
const quitArmNotice = "ctrl+c again to quit"

// quitArmed reports whether a quit is currently armed.
func (m *tuiModel) quitArmed() bool {
	return !m.quitArmedAt.IsZero() && timeNow().Sub(m.quitArmedAt) <= quitArmWindow
}

// armQuit arms the quit and says so where the user is looking.
func (m *tuiModel) armQuit() {
	m.quitArmedAt = timeNow()
}

// disarmQuit clears an armed quit. Called for every key that is not the
// arming one: an arm that survives other input is a trap.
func (m *tuiModel) disarmQuit() {
	if m.quitArmedAt.IsZero() {
		return
	}
	m.quitArmedAt = time.Time{}
}

// handleModalEscapeKey gives ctrl+q and ctrl+c their meaning even while a
// dialog or overlay owns the screen. Both are documented as global, and the
// UI invites the user to open the fleet overlay (ctrl+g) mid-turn - so a
// modal that swallowed them left no way to stop a running turn without first
// knowing to press esc.
//
// Returns handled=false for every other key, so modal routing is unchanged.
func (m *tuiModel) handleModalEscapeKey(key string) ([]tea.Cmd, bool) {
	modalOpen := m.overlay != nil || m.modelDlg != nil || m.effortDlg != nil || m.worktreeDlg != nil
	switch key {
	case "ctrl+q":
		return []tea.Cmd{tea.Quit}, true
	case "ctrl+c":
		if !modalOpen {
			return nil, false
		}
		// Close the surface first: the cancel is about the turn underneath,
		// and leaving a dialog over a cancelled transcript hides the result.
		m.closeModal()
		_, _, cmds := m.handleChatCancel()
		return cmds, true
	}
	return nil, false
}

// handleIdleCancel runs the idle branch of ctrl+c. Returns the standard
// (skipTextarea, skipViewport, cmds) triple.
func (m *tuiModel) handleIdleCancel() (bool, bool, []tea.Cmd) {
	if m.quitArmed() {
		return true, true, []tea.Cmd{tea.Quit}
	}
	// A selection is the most likely intent behind ctrl+c at rest, in either
	// focus: the key sits right next to the message the user just picked.
	if m.selectedBlockID != "" {
		if cmd, ok := m.copySelectedBlock(); ok {
			m.selectedBlockID = ""
			m.renderVP()
			if cmd == nil {
				return true, true, nil
			}
			return true, true, []tea.Cmd{cmd}
		}
	}
	if strings.TrimSpace(m.textarea.Value()) != "" {
		m.textarea.Reset()
		m.armQuit()
		return true, true, nil
	}
	m.armQuit()
	return true, true, nil
}

// takeQueuedSlashCmds drains terminal commands produced by slash commands
// that ran from the pending queue, where sendNextQueued has no tea.Cmd
// return of its own.
func (m *tuiModel) takeQueuedSlashCmds() []tea.Cmd {
	if len(m.queuedSlashCmds) == 0 {
		return nil
	}
	cmds := m.queuedSlashCmds
	m.queuedSlashCmds = nil
	return cmds
}

// handleChatCancel handles the ctrl+c key for cancelling the current turn or quitting.
// Cancel preserves the partial story (interim, status, tools) and appends a
// cancelled footer - web-like stop, not wipe (Phase E).
//
// Stage 1 (waiting): cancel the current turn, show cancelled state.
// Stage 2 (cancelling, agent still unwinding): set quitRequested; quit when
// agentDone (worker Finish already drained) or when the next Done arrives.
// Stage 2b (quitRequested already): force Quit - never strand on hung tools.
// Stage 3 (fully idle): quit immediately.
func (m *tuiModel) handleChatCancel() (bool, bool, []tea.Cmd) {
	if m.waiting {
		// Stage 1: first Ctrl+C - cancel the turn.
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
			// alone - worker may still be running tools.
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
			// Quit immediately - waiting for another Done strands the session.
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
	// Stage 3: fully idle - copy a selection, protect a draft, or arm the
	// quit (tui_cancel.go). Never an unguarded exit on one keystroke.
	return m.handleIdleCancel()
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
