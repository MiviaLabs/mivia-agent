package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Copying out of the TUI was impossible: mouse capture takes the terminal's
// native selection away (including in the composer), and nothing in the app
// replaced it.

func TestOSC52Encoding(t *testing.T) {
	seq := osc52Copy("hello world")
	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("not an OSC 52 sequence: %q", seq)
	}
	if !strings.Contains(seq, "aGVsbG8gd29ybGQ=") {
		t.Fatalf("payload not base64: %q", seq)
	}
	// Oversized payloads are refused rather than truncated into garbage that
	// some terminals would paste as a partial string.
	if osc52Copy(strings.Repeat("x", osc52MaxBytes+1)) != "" {
		t.Fatal("oversized payload must be refused")
	}
	if osc52Copy("") != "" {
		t.Fatal("empty copy must be a no-op")
	}
}

func TestCopyBlockTextIsPlain(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.blocks = []ChatBlock{
		{ID: "a1", Kind: ChatBlockAssistant, Text: "the **answer** is 42"},
	}
	m.renderVP()
	m.selectedBlockID = "a1"

	text, ok := m.selectedBlockCopyText()
	if !ok {
		t.Fatal("selected block must be copyable")
	}
	// The source text, not the rendered ANSI — pasting escape codes into an
	// editor is worse than useless.
	if text != "the **answer** is 42" {
		t.Fatalf("copy text = %q, want the block source", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("copy text carries ANSI: %q", text)
	}
}

func TestYankKeyCopiesSelectedBlock(t *testing.T) {
	withWorkingClipboard(t)
	m := newReadyChatModel(30, 80)
	m.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "copy me"}}
	m.renderVP()
	m.focus = focusScrollback
	m.selectedBlockID = "a1"

	skipTA, _, cmds := m.handleChatKey("y", false)
	if !skipTA {
		t.Fatal("y must be consumed in scrollback focus")
	}
	// The acknowledgement reports what delivery actually achieved, so it
	// arrives with the copy result rather than being claimed up front.
	runCopyCmds(t, m, cmds)
	if !strings.Contains(m.notice, "copied") {
		t.Fatalf("copy must be acknowledged: %q", m.notice)
	}
	// While composing, 'y' stays a typable letter.
	m2 := newReadyChatModel(30, 80)
	m2.focus = focusComposer
	if skip, _, _ := m2.handleChatKey("y", false); skip {
		t.Fatal("y while composing must reach the textarea")
	}
}

func TestCtrlCCopiesOnlyWhenIdleWithSelection(t *testing.T) {
	// ctrl+c stays the terminal's cancel/quit convention. It copies only in
	// the one unambiguous case: idle, scrollback focus, block selected.
	withWorkingClipboard(t)
	m := newReadyChatModel(30, 80)
	m.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "copy me"}}
	m.renderVP()
	m.focus = focusScrollback
	m.selectedBlockID = "a1"
	m.waiting = false
	_, _, cmds := m.handleChatKey("ctrl+c", false)
	runCopyCmds(t, m, cmds)
	if !strings.Contains(m.notice, "copied") {
		t.Fatalf("idle ctrl+c with a selection should copy: %q", m.notice)
	}

	// Mid-turn it must still cancel — never copy.
	m2 := newReadyChatModel(30, 80)
	m2.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "x"}}
	m2.renderVP()
	m2.focus = focusScrollback
	m2.selectedBlockID = "a1"
	m2.waiting = true
	m2.handleChatKey("ctrl+c", false)
	if strings.Contains(m2.notice, "copied") {
		t.Fatal("ctrl+c during a turn must cancel, not copy")
	}
	if !m2.cancelling && m2.waiting {
		t.Fatal("ctrl+c during a turn must start a cancel")
	}
}

func TestRightClickCopiesBlock(t *testing.T) {
	withWorkingClipboard(t)
	m := newReadyChatModel(30, 80)
	m.blocks = []ChatBlock{{ID: "a1", Kind: ChatBlockAssistant, Text: "right click me"}}
	m.layout()
	m.renderVP()
	m.View()
	rng, ok := m.chatBlockRanges["a1"]
	if !ok {
		t.Fatal("block range missing")
	}
	y := rng[0] + 1 - m.viewport.YOffset // +1 for the status header
	_, cmd := m.Update(tea.MouseMsg{X: 2, Y: y, Type: tea.MouseRight})
	runCopyCmds(t, m, []tea.Cmd{cmd})
	if !strings.Contains(m.notice, "copied") {
		t.Fatalf("right click should copy the block under the cursor: %q", m.notice)
	}
}

func TestSelectModeReleasesTheMouse(t *testing.T) {
	// The only way to select arbitrary text (and anything in the composer) is
	// to hand the mouse back to the terminal.
	m := newReadyChatModel(30, 80)
	m.mouseEnabled = true
	m.handleChatKey("f2", false)
	if m.mouseEnabled {
		t.Fatal("select mode must release mouse capture")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "select mode") {
		t.Fatalf("select mode must be visible in the chrome:\n%s", view)
	}
	m.handleChatKey("f2", false)
	if !m.mouseEnabled {
		t.Fatal("f2 must toggle capture back on")
	}
}

func TestCtrlQQuits(t *testing.T) {
	// ctrl+c keeps its cancel-then-quit meaning, so an unambiguous quit key
	// is worth having next to it.
	m := newReadyChatModel(30, 80)
	m.waiting = false
	_, _, cmds := m.handleChatKey("ctrl+q", false)
	if len(cmds) == 0 {
		t.Fatal("ctrl+q must issue a quit command")
	}
}

func TestSelectModeKeyAvoidsFlowControl(t *testing.T) {
	// ctrl+s is XOFF: with software flow control on (tmux, many terminals,
	// any session where raw mode did not clear IXON) it freezes output
	// instead of reaching the app. Select mode must not be bound to it — nor
	// to ctrl+e, which the composer needs for line-end.
	m := newReadyChatModel(30, 80)
	m.mouseEnabled = true
	m.handleChatKey("f2", false)
	if m.mouseEnabled {
		t.Fatal("f2 must toggle select mode")
	}
	m3 := newReadyChatModel(30, 80)
	m3.mouseEnabled = true
	m3.handleChatKey("ctrl+e", false)
	if !m3.mouseEnabled {
		t.Fatal("ctrl+e must NOT toggle select mode: the composer needs it for line-end")
	}
	m2 := newReadyChatModel(30, 80)
	m2.mouseEnabled = true
	m2.handleChatKey("ctrl+s", false)
	if !m2.mouseEnabled {
		t.Fatal("ctrl+s must NOT be bound: it is terminal flow control (XOFF)")
	}
}

func TestClipboardPrefersLocalToolThenOSC52(t *testing.T) {
	// OSC 52 is refused by default in several terminals and multiplexers, so
	// a local clipboard binary is tried first when one exists. OSC 52 remains
	// the fallback because it is the only thing that works over SSH.
	if cmd := clipboardToolCommand("hi"); cmd != nil {
		if len(cmd.Args) == 0 {
			t.Fatal("clipboard tool command has no argv")
		}
	}
	// Whatever the environment, copying must produce SOME delivery path.
	if copyToClipboardCmd("hi") == nil {
		t.Fatal("copy must always have at least the OSC 52 path")
	}
}
