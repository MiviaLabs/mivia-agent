package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Idle ctrl+c used to be an unguarded exit. Selecting a message, clicking back
// into the composer and pressing ctrl+c quit the app outright — the copy guard
// required scrollback focus, so the one key most people press reflexively
// destroyed the session view instead of copying. And with a half-typed
// question in the composer, a single keystroke threw it away with no warning.
//
// Idle precedence: copy a selection · else clear a draft and arm · else arm,
// and quit on the second press.

func idleChatModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = false
	return m
}

// TestCtrlCCopiesSelectionFromComposerFocus is the reported foot-gun.
func TestCtrlCCopiesSelectionFromComposerFocus(t *testing.T) {
	withWorkingClipboard(t)
	m := idleChatModel(t)
	m.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "copy me"}}
	m.renderVP()
	m.selectedBlockID = "a1"
	m.setFocus(focusComposer) // e.g. clicked back to keep typing

	_, _, cmds := m.handleChatKey("ctrl+c", false)

	if cmdsContainQuit(cmds) {
		t.Fatal("ctrl+c with a selected message must copy, not quit the app")
	}
	runCopyCmds(t, m, cmds)
	if !strings.Contains(m.notice, "copied") {
		t.Fatalf("ctrl+c must copy the selection: %q", m.notice)
	}
	// The selection is consumed, so a second ctrl+c can still reach quit
	// instead of copying the same block forever.
	if m.selectedBlockID != "" {
		t.Fatal("copying via ctrl+c must clear the selection")
	}
}

// TestCtrlCClearsDraftBeforeQuitting: a half-typed question must not be lost
// to one keystroke.
func TestCtrlCClearsDraftBeforeQuitting(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(focusComposer)
	m.textarea.SetValue("half a question")

	_, _, cmds := m.handleChatKey("ctrl+c", false)

	if cmdsContainQuit(cmds) {
		t.Fatal("ctrl+c with a draft must clear the draft, not quit")
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("ctrl+c must clear the draft, got %q", got)
	}
	if !strings.Contains(stripANSI(m.View()), "again to quit") {
		t.Fatal("clearing the draft must say how to actually quit")
	}
}

// TestCtrlCArmsThenQuits: from a clean idle state, the first press arms and
// the second quits.
func TestCtrlCArmsThenQuits(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(focusComposer)

	_, _, cmds := m.handleChatKey("ctrl+c", false)
	if cmdsContainQuit(cmds) {
		t.Fatal("the first idle ctrl+c must arm, not quit")
	}
	if !strings.Contains(stripANSI(m.View()), "again to quit") {
		t.Fatal("an armed quit must be visible")
	}

	_, _, cmds2 := m.handleChatKey("ctrl+c", false)
	if !cmdsContainQuit(cmds2) {
		t.Fatal("the second ctrl+c must quit")
	}
}

// TestCtrlCArmExpires: the arm is a moment, not a mode. After the window a
// fresh press must arm again rather than quitting out of nowhere.
func TestCtrlCArmExpires(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(focusComposer)
	_, _, _ = m.handleChatKey("ctrl+c", false)
	m.quitArmedAt = time.Now().Add(-quitArmWindow - time.Second)

	_, _, cmds := m.handleChatKey("ctrl+c", false)
	if cmdsContainQuit(cmds) {
		t.Fatal("a stale arm must not quit; the press must re-arm instead")
	}
}

// TestCtrlCArmClearedByOtherInput: typing after an armed quit must disarm, or
// ctrl+c much later exits with no warning.
func TestCtrlCArmClearedByOtherInput(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(focusComposer)
	_, _, _ = m.handleChatKey("ctrl+c", false)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	_, _, cmds := m.handleChatKey("ctrl+c", false)
	if cmdsContainQuit(cmds) {
		t.Fatal("input after arming must disarm the quit")
	}
}

// TestCtrlCDuringTurnStillCancels: the busy path is untouched — cancel must
// never become a quit or a copy.
func TestCtrlCDuringTurnStillCancels(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "x"}}
	m.renderVP()
	m.selectedBlockID = "a1"
	m.textarea.SetValue("draft while busy")
	m.waiting = true
	m.turnStart = time.Now()

	_, _, cmds := m.handleChatKey("ctrl+c", false)

	if cmdsContainQuit(cmds) {
		t.Fatal("ctrl+c during a turn must cancel, never quit")
	}
	if strings.Contains(m.notice, "copied") {
		t.Fatal("ctrl+c during a turn must cancel, never copy")
	}
	if !m.cancelling {
		t.Fatal("ctrl+c during a turn must start a cancel")
	}
}

// TestSelectModeOnF2 moves select mode off ctrl+e, which bubbles binds to
// line-end: with home/end restored to the composer, ctrl+e was the last key
// standing between the user and the end of their own line.
func TestSelectModeOnF2(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.mouseEnabled = true

	m.handleChatKey("f2", false)
	if m.mouseEnabled {
		t.Fatal("f2 must release mouse capture (select mode)")
	}
	m.handleChatKey("f2", false)
	if !m.mouseEnabled {
		t.Fatal("f2 must restore mouse capture")
	}
}

// TestCtrlEIsLineEndNotSelectMode: the composer gets its line-end key back.
func TestCtrlEIsLineEndNotSelectMode(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.mouseEnabled = true
	m.setFocus(focusComposer)
	m.textarea.SetValue("hello world")
	m.textarea.CursorStart()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})

	if !m.mouseEnabled {
		t.Fatal("ctrl+e must no longer toggle select mode")
	}
	if got := m.textarea.LineInfo().ColumnOffset; got == 0 {
		t.Fatal("ctrl+e must move the cursor to line end")
	}
}

// TestSelectSlashCommandTogglesMode gives select mode a discoverable route
// that needs no function key (some terminals and multiplexers eat F2).
func TestSelectSlashCommandTogglesMode(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.mouseEnabled = true

	if !m.handleSlash("/select") {
		t.Fatal("/select must be a recognised command")
	}
	if m.mouseEnabled {
		t.Fatal("/select must release mouse capture")
	}
}

// TestWelcomeCtrlQQuits: ctrl+q quits everywhere, not only in chat.
func TestWelcomeCtrlQQuits(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})

	if cmd == nil {
		t.Fatal("ctrl+q on the welcome screen must quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatal("ctrl+q on the welcome screen must quit")
	}
}
