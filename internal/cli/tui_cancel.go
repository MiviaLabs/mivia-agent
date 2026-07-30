package cli

// The idle ctrl+c state machine.
//
// ctrl+c keeps its terminal meaning while a turn is running: it cancels, and
// nothing else (handleChatCancel). At rest it used to quit on the first press,
// which made two everyday actions destructive — selecting a message and then
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
// UI invites the user to open the fleet overlay (ctrl+g) mid-turn — so a
// modal that swallowed them left no way to stop a running turn without first
// knowing to press esc.
//
// Returns handled=false for every other key, so modal routing is unchanged.
func (m *tuiModel) handleModalEscapeKey(key string) ([]tea.Cmd, bool) {
	modalOpen := m.sessionsDlg != nil || m.overlay != nil
	switch key {
	case "ctrl+q":
		return []tea.Cmd{tea.Quit}, true
	case "ctrl+c":
		if !modalOpen {
			return nil, false
		}
		// Close the surface first: the cancel is about the turn underneath,
		// and leaving a dialog over a cancelled transcript hides the result.
		m.sessionsDlg = nil
		m.overlay = nil
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
