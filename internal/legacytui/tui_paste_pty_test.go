//go:build linux

package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"
)

// Real bracketed-paste bytes through a Linux PTY and the tea.Program input
// path. Unit tests can synthesize a KeyMsg with Paste=true, but only the byte
// path proves the terminal contract: that the payload arrives as ONE message
// (never N keypresses), that embedded newlines do not reach the Enter handler
// and fire N sends, and that a payload larger than bubbletea's 256-byte read
// buffer is reassembled instead of truncated.

// writePaste wraps text in the bracketed-paste markers a terminal sends.
func (h *ptyScrollHarness) writePaste(text string) {
	h.t.Helper()
	h.writeCSI("\x1b[200~" + text + "\x1b[201~")
}

func TestScrollPTY_BracketedPasteInsertsWithoutSending(t *testing.T) {
	h := startPTYScrollProgram(t, func(m *TUIModel) {
		m.setFocus(cli.FocusComposer)
		m.waiting = false
	})
	h.writePaste("line one\nline two\nline three")

	if !h.wait(3*time.Second, func(m *TUIModel) bool {
		return strings.Count(m.textarea.Value(), "\n") == 2
	}) {
		t.Fatal("multi-line bracketed paste did not insert all lines")
	}
	// The classic failure: newlines interpreted as Enter, sending N messages.
	if !h.wait(time.Second, func(m *TUIModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0
	}) {
		t.Fatal("bracketed paste triggered a send instead of inserting")
	}
}

func TestScrollPTY_LargePasteIsNotTruncated(t *testing.T) {
	// Larger than bubbletea's 256-byte input read buffer, so the paste spans
	// several reads and exercises the short-read reassembly path.
	const line = "abcdefghijklmnopqrstuvwxyz0123456789"
	want := strings.Repeat(line, 40) // ~1440 bytes
	h := startPTYScrollProgram(t, func(m *TUIModel) {
		m.setFocus(cli.FocusComposer)
		m.waiting = false
	})
	h.writePaste(want)

	if !h.wait(5*time.Second, func(m *TUIModel) bool {
		return m.textarea.Value() == want
	}) {
		t.Fatal("large bracketed paste was truncated or split")
	}
}

func TestScrollPTY_PasteWhileScrollbackFocusedIsNotLost(t *testing.T) {
	h := startPTYScrollProgram(t, func(m *TUIModel) {
		m.setFocus(cli.FocusScrollback)
	})
	h.writePaste("recovered paste")

	if !h.wait(3*time.Second, func(m *TUIModel) bool {
		return m.textarea.Value() == "recovered paste" && m.focus == cli.FocusComposer
	}) {
		t.Fatal("paste with the transcript focused was dropped instead of refocusing the composer")
	}
}
