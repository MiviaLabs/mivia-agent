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

// takePendingSlashCmds drains commands a slash handler asked for. Slash
// handlers report only "handled"; a command they need (select mode releasing
// the mouse) is staged here and collected by the caller that owns the cmds.
func (m *tuiModel) takePendingSlashCmds() []tea.Cmd {
	if !m.pendingSelectToggle {
		return nil
	}
	m.pendingSelectToggle = false
	return []tea.Cmd{m.toggleSelectMode()}
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
	m.stepDetail = quitArmNotice
	m.stepDetailAt = m.quitArmedAt
}

// disarmQuit clears an armed quit. Called for every key that is not the
// arming one: an arm that survives other input is a trap.
func (m *tuiModel) disarmQuit() {
	if m.quitArmedAt.IsZero() {
		return
	}
	m.quitArmedAt = time.Time{}
	if m.stepDetail == quitArmNotice {
		m.stepDetail = ""
	}
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
