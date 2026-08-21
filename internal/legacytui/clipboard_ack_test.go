package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A copy that says nothing is indistinguishable from a broken key, and a copy
// that claims success when the clipboard tool failed is worse: the user walks
// away and pastes stale content. The acknowledgement must be visible at idle
// (the normal case for copying) and must report what actually happened.

// withWorkingClipboard makes copy delivery deterministic: a wl-copy stub that
// succeeds, and an OSC 52 sink that is a plain file rather than a terminal
// (tests have no controlling tty, and a real /dev/tty write would leak escape
// sequences into the test output).
func withWorkingClipboard(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeFakeTool(t, dir, "wl-copy", "/bin/cat > /dev/null")
	t.Setenv("PATH", dir)
	t.Setenv("MIVIA_CLIPBOARD_TTY", filepath.Join(dir, "tty-sink"))
}

// TestCopyAckVisibleWhenIdle: the ack was written to stepDetail, which the
// composer chrome rendered only while waiting - so every copy made outside a
// turn was silent.
func TestCopyAckVisibleWhenIdle(t *testing.T) {
	withWorkingClipboard(t)
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = false
	m.blocks = []cli.ChatBlock{{ID: "a1", Kind: cli.ChatBlockAssistant, Text: "the answer"}}
	m.renderVP()
	m.selectedBlockID = "a1"
	m.setFocus(cli.FocusScrollback)

	cmd, ok := m.copySelectedBlock()
	if !ok {
		t.Fatal("copy must succeed with a block selected")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			_, _ = m.Update(msg)
		}
	}

	if !strings.Contains(cli.StripANSI(m.View()), "copied") {
		t.Fatal("an idle copy gives no visible acknowledgement")
	}
}

// TestCopyAckExpires: a notice that never clears becomes furniture and then
// becomes a lie about a copy made minutes ago. Copy acks live in the notice
// chrome (m.notice/m.noticeAt, not the tool-heartbeat stepDetail field), so
// the stale-notice leg must backdate noticeAt past the TTL.
func TestCopyAckExpires(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = false
	m.setNotice(copiedNotice(11))
	m.noticeAt = time.Now().Add(-copyNoticeTTL - time.Second)

	if n := m.freshNotice(); n != "" {
		t.Fatalf("stale copy notice still fresh: %q", n)
	}
	if strings.Contains(cli.StripANSI(m.View()), "copied") {
		t.Fatal("a stale copy notice must expire from the chrome")
	}
}

// TestCopyAckReportsToolFailure: cmd.Run()'s error was discarded, so a
// wl-copy present but with no compositor reported "copied N chars" for a copy
// that never landed.
func TestCopyAckReportsToolFailure(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "wl-copy", "exit 1")
	t.Setenv("PATH", dir)
	// No TTY in tests, so the OSC 52 path cannot land either: the copy really
	// did fail and must say so.
	t.Setenv("MIVIA_CLIPBOARD_TTY", "/nonexistent-tty")

	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = false
	m.blocks = []cli.ChatBlock{{ID: "a1", Kind: cli.ChatBlockAssistant, Text: "the answer"}}
	m.renderVP()
	m.selectedBlockID = "a1"

	cmd, ok := m.copySelectedBlock()
	if !ok {
		t.Fatal("copy must be attempted")
	}
	if cmd == nil {
		t.Fatal("copy must produce a delivery command")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("copy command must report its outcome")
	}
	_, _ = m.Update(msg)

	plain := cli.StripANSI(m.View())
	if strings.Contains(plain, "copied") {
		t.Fatalf("a failed copy claimed success: %q", plain)
	}
	if !strings.Contains(plain, "copy failed") {
		t.Fatalf("a failed copy must say so: %q", plain)
	}
}

// TestCopyLargeTextUsesLocalToolBeyondOSC52Limit: the size gate belongs to
// OSC 52 (terminals truncate or drop big payloads), not to the local binary,
// which has no such limit. Refusing to copy at all when wl-copy is installed
// was a self-inflicted restriction.
func TestCopyLargeTextUsesLocalToolBeyondOSC52Limit(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "wl-copy", "/bin/cat > /dev/null")
	t.Setenv("PATH", dir)

	big := strings.Repeat("x", osc52MaxBytes+1)
	if osc52Copy(big) != "" {
		t.Fatal("precondition: OSC 52 must still refuse an oversized payload")
	}
	if copyToClipboardCmd(big) == nil {
		t.Fatal("a local clipboard tool must still copy text too large for OSC 52")
	}
}

// TestCopyRightClickAckVisible covers the mouse path, which sets the notice
// through a different entry point.
func TestCopyRightClickAckVisible(t *testing.T) {
	withWorkingClipboard(t)
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = false
	m.blocks = []cli.ChatBlock{{ID: "a1", Kind: cli.ChatBlockAssistant, Text: "the answer"}}
	m.renderVP()

	cmd, ok := m.copyBlockByID("a1")
	if !ok {
		t.Fatal("right-click copy must succeed for a real block")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			_, _ = m.Update(msg)
		}
	}
	if !strings.Contains(cli.StripANSI(m.View()), "copied") {
		t.Fatal("right-click copy gives no visible acknowledgement")
	}
}

// runCopyCmds executes copy delivery commands and feeds their results back
// into the model, the way the bubbletea loop does. The acknowledgement is
// deliberately not set until delivery reports its outcome.
func runCopyCmds(t *testing.T, m *TUIModel, cmds []tea.Cmd) {
	t.Helper()
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		if msg := cmd(); msg != nil {
			_, _ = m.Update(msg)
		}
	}
}
