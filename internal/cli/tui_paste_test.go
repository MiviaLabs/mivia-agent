package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// writeFakeTool installs an executable shell stub on PATH for clipboard tests.
func writeFakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// Pasted input must never be silently dropped. bubbles' textarea ignores every
// key while blurred, and routeFocusKey's "printable input returns focus to the
// composer" rule cannot fire for a paste: bubbletea wraps pasted runes in
// "[...]" precisely so bindings cannot match, so the key string is multi-rune
// and isPrintableKey is false. Clicking a message and pasting therefore lost
// the clipboard contents with no feedback at all.

func pasteMsg(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true}
}

// TestPasteWhileScrollbackFocusedReachesComposer is the core regression.
func TestPasteWhileScrollbackFocusedReachesComposer(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusScrollback)

	_, _ = m.Update(pasteMsg("pasted text"))

	if got := m.textarea.Value(); got != "pasted text" {
		t.Fatalf("paste was dropped while the transcript had focus: composer=%q", got)
	}
	if m.focus != focusComposer {
		t.Fatalf("paste must return focus to the composer, focus=%v", m.focus)
	}
}

// TestPasteAppendsToExistingDraft: refocusing must not clobber a draft.
func TestPasteAppendsToExistingDraft(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusComposer)
	m.textarea.SetValue("before ")
	m.setFocus(focusScrollback)

	_, _ = m.Update(pasteMsg("after"))

	if got := m.textarea.Value(); got != "before after" {
		t.Fatalf("paste clobbered the draft: %q", got)
	}
}

// TestMultilinePasteDoesNotSend proves the classic failure is absent: a
// multi-line paste must insert as one edit, never fire N sends.
func TestMultilinePasteDoesNotSend(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.waiting = false
	m.setFocus(focusComposer)

	_, _ = m.Update(pasteMsg("line1\nline2\nline3"))

	if m.waiting {
		t.Fatal("a multi-line paste sent the message instead of inserting it")
	}
	if got := m.textarea.Value(); strings.Count(got, "\n") != 2 {
		t.Fatalf("multi-line paste did not insert all lines: %q", got)
	}
	if len(m.pendingQueue) != 0 {
		t.Fatalf("paste queued %d messages", len(m.pendingQueue))
	}
}

// TestPasteStaysSwallowedWhileModalOpen: an overlay owns the screen, so a
// paste must not leak into the composer behind it.
func TestPasteStaysSwallowedWhileModalOpen(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusScrollback)
	m.overlay = newDialog("test", []string{"line"})

	_, _ = m.Update(pasteMsg("must not land"))

	if got := m.textarea.Value(); got != "" {
		t.Fatalf("paste leaked past the overlay into the composer: %q", got)
	}
	if m.overlay == nil {
		t.Fatal("paste must not dismiss the overlay")
	}
}

// TestPasteInWelcomeModeInserts: the welcome screen shares the composer.
func TestPasteInWelcomeModeInserts(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome

	_, _ = m.Update(pasteMsg("welcome paste"))

	if got := m.textarea.Value(); got != "welcome paste" {
		t.Fatalf("welcome-mode paste dropped: %q", got)
	}
}

// TestClipboardReadCommandPrefersAvailableTool pins the ctrl+v read path:
// bubbles routes ctrl+v to atotto/clipboard, which knows only X11 binaries —
// on Wayland it fails, and the error lands in textarea.Err where nothing in
// the app ever reads it. mivia reads the clipboard itself, with the same tool
// list it writes with, and reports failure where the user can see it.
func TestClipboardReadCommandPrefersAvailableTool(t *testing.T) {
	dir := t.TempDir()
	// A fake wl-paste that is earlier in clipboardReadTools than xclip.
	writeFakeTool(t, dir, "wl-paste", "/usr/bin/printf 'from wl-paste'")
	t.Setenv("PATH", dir)

	text, err := readClipboardText()
	if err != nil {
		t.Fatalf("readClipboardText: %v", err)
	}
	if text != "from wl-paste" {
		t.Fatalf("clipboard text = %q, want %q", text, "from wl-paste")
	}
}

// TestClipboardReadReportsFailureWhenNoToolExists: silence is the bug.
func TestClipboardReadReportsFailureWhenNoToolExists(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if _, err := readClipboardText(); err == nil {
		t.Fatal("readClipboardText must report an error when no clipboard tool exists")
	}
}

// TestCtrlVFailureIsVisible: a failed paste must say so in the chrome, not
// vanish. This is the whole reason the atotto path was invisible.
func TestCtrlVFailureIsVisible(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusComposer)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("ctrl+v must return a command that reads the clipboard")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("clipboard read command produced no message")
	}
	_, _ = m.Update(msg)

	if m.stepDetail == "" {
		t.Fatal("a failed ctrl+v must be visible in the chrome, not silent")
	}
}
