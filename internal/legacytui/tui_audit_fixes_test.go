package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Regressions found by the hostile audit of the input-layer rebuild.

// ── Armed quit must be cleared by every kind of input, not just keys ──────
// disarmQuit lived in handleChatKey, which paste and mouse events never
// reach, so an arm survived them and the next ctrl+c exited the app - the
// exact foot-gun the arm was introduced to remove.

func TestCtrlCArmClearedByPaste(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(cli.FocusComposer)
	m.textarea.SetValue("half a question")
	_, _, _ = m.handleChatKey("ctrl+c", false) // clears draft, arms

	_, _ = m.Update(pasteMsg("pasted work"))

	_, _, cmds := m.handleChatKey("ctrl+c", false)
	if cmdsContainQuit(cmds) {
		t.Fatal("pasting after an arm must disarm it: ctrl+c quit with a live draft")
	}
}

func TestCtrlCArmClearedByMouseSelection(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.waiting = false
	m.blocks = []cli.ChatBlock{{ID: "a1", Kind: cli.ChatBlockAssistant, Text: "copy me"}}
	m.renderVP()
	_ = m.View() // build the hit map so the click lands on a block
	m.setFocus(cli.FocusComposer)
	_, _, _ = m.handleChatKey("ctrl+c", false) // arms

	// The real path: a mouse event through Update, not a direct call.
	_, _ = m.Update(tea.MouseMsg{X: 2, Y: transcriptMouseY(m), Type: tea.MouseLeft})

	_, _, cmds := m.handleChatKey("ctrl+c", false)
	if cmdsContainQuit(cmds) {
		t.Fatal("clicking a message after an arm must disarm it: ctrl+c quit instead of copying")
	}
}

// ── Cancel and quit must survive modal surfaces ──────────────────────────
// ctrl+g opens the fleet overlay mid-turn - the hint line says so - and the
// overlay consumed every key, so the one key that must always work could not
// stop a runaway turn.

func TestCtrlCCancelsWithFleetOverlayOpen(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.overlay = newDialog("fleet", []string{"◆ explorer"})

	_, _, _ = m.handleChatKey("ctrl+c", false)

	if !m.cancelling {
		t.Fatal("ctrl+c must cancel the running turn even with an overlay open")
	}
}

func TestCtrlQQuitsFromEveryModalSurface(t *testing.T) {
	for name, open := range map[string]func(*TUIModel){
		"overlay": func(m *TUIModel) { m.overlay = newDialog("x", []string{"y"}) },
	} {
		m := newReadyChatModel(30, 80)
		m.mode = modeChat
		open(m)

		_, _, cmds := m.handleChatKey("ctrl+q", false)

		if !cmdsContainQuit(cmds) {
			t.Errorf("ctrl+q must quit with the %s open", name)
		}
	}
}

// ── /select must act where it is invoked ─────────────────────────────────
// The handler only staged a flag that one caller drained, so /select did
// nothing from the queue or the welcome screen and then fired later on an
// unrelated command.

func TestQueuedSelectTogglesImmediately(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.mouseEnabled = true
	m.pendingQueue = []string{"/select"}

	m.sendNextQueued()

	if m.mouseEnabled {
		t.Fatal("/select from the queue must release mouse capture")
	}
	if m.pendingSelectCmd != nil {
		t.Fatal("/select must not leave a staged command to fire on a later command")
	}
}

func TestWelcomeSelectTogglesImmediately(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome
	m.mouseEnabled = true

	_ = m.handleWelcomeEnter("/select")

	if m.mouseEnabled {
		t.Fatal("/select on the welcome screen must release mouse capture")
	}
	if m.pendingSelectCmd != nil {
		t.Fatal("/select must not leave a staged command behind")
	}
}

// ── Clipboard write must try every installed tool ────────────────────────
// The read path already falls through a failing tool; the write path stopped
// at the first one found and then reported "no clipboard tool reachable".

func TestCopyFallsBackToNextInstalledTool(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "wl-copy", "exit 1")
	writeFakeTool(t, dir, "xclip", "/bin/cat > /dev/null")
	t.Setenv("PATH", dir)
	t.Setenv("MIVIA_CLIPBOARD_TTY", "/nonexistent-tty")

	cmd := copyToClipboardCmd("some text")
	if cmd == nil {
		t.Fatal("copy must produce a delivery command")
	}
	res, ok := cmd().(copyResultMsg)
	if !ok {
		t.Fatalf("expected copyResultMsg, got %T", cmd())
	}
	if res.err != nil {
		t.Fatalf("copy must fall through a failing tool to a working one: %v", res.err)
	}
}

// ── Transient notices must not evict live turn progress ──────────────────
// Copy/paste acknowledgements shared stepDetail with the tool heartbeat, so
// copying mid-turn replaced the only progress indicator with a string that
// never expired on the footer or status bar.

func TestCopyDuringTurnKeepsHeartbeat(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.stepDetail = "tools 1/3 done · 12s"
	m.stepDetailAt = time.Now()

	_, _ = m.Update(copyResultMsg{n: 7})

	if m.stepDetail != "tools 1/3 done · 12s" {
		t.Fatalf("a copy replaced the live tool heartbeat: %q", m.stepDetail)
	}
}

// ── The arm prompt must vanish with the arm ──────────────────────────────
func TestArmNoticeDisappearsWithTheArm(t *testing.T) {
	m := idleChatModel(t)
	m.setFocus(cli.FocusComposer)
	_, _, _ = m.handleChatKey("ctrl+c", false)
	m.quitArmedAt = time.Now().Add(-quitArmWindow - time.Millisecond)

	if strings.Contains(cli.StripANSI(m.View()), "again to quit") {
		t.Fatal("the arm prompt outlived the arm: it promises an exit the next press will not deliver")
	}
}

// ── end must move the caret in a whitespace-only draft ───────────────────
func TestEndMovesCaretWithWhitespaceOnlyDraft(t *testing.T) {
	m := composerDraftModel(t, "      ")
	m.textarea.CursorStart()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})

	if got := m.textarea.LineInfo().ColumnOffset; got == 0 {
		t.Fatal("end must reach line end in a whitespace-only draft: there is a line to move within")
	}
}

// ── Every key the router honours must be registered ──────────────────────
// validateKeyRegistry checked registry→router. Nothing checked router→
// registry, which is the direction that produces undocumented keys.

func TestEveryBoundKeyIsRegistered(t *testing.T) {
	registered := map[string]bool{}
	for _, b := range cli.KeyRegistry {
		for _, k := range b.Keys {
			registered[b.Scope.String()+"/"+k] = true
		}
	}
	for _, c := range boundKeyProbes(t) {
		if registered[c.scope.String()+"/"+c.key] {
			continue
		}
		t.Errorf("%s honours %q but cli.KeyRegistry does not declare it, so /help never mentions it",
			c.scope, c.key)
	}
}
