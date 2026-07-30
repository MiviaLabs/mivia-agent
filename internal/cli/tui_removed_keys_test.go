package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// removedEditingKeys are bindings mivia no longer honours. bubbles' textarea
// binds them to destructive edits (ctrl+u delete-before-cursor, ctrl+k
// delete-after-cursor, ctrl+w delete-word-backward) and the viewport binds
// ctrl+u to half-page-up, so a single key both edited the draft and scrolled
// depending on which pane had focus - and all three did nothing at all while the
// composer was blurred. alt+backspace still deletes a word.
var removedEditingKeys = []string{"ctrl+u", "ctrl+k", "ctrl+w"}

// TestScrollAccept_RemovedEditingKeysAreInert asserts the removed keys reach
// neither the composer nor the transcript, in either focus.
func TestScrollAccept_RemovedEditingKeysAreInert(t *testing.T) {
	for _, key := range removedEditingKeys {
		for _, focus := range []tuiFocus{focusComposer, focusScrollback} {
			m := tallScrollModel(t, 6, 50)
			m.setFocus(focus)
			m.textarea.SetValue("keep this draft")

			skipTextarea, skipViewport, cmds := m.handleChatKey(key, false)
			if !skipTextarea {
				t.Fatalf("%q (focus %v) must not reach the composer", key, focus)
			}
			if !skipViewport {
				t.Fatalf("%q (focus %v) must not reach the transcript", key, focus)
			}
			if len(cmds) != 0 {
				t.Fatalf("%q (focus %v) must not emit commands, got %d", key, focus, len(cmds))
			}
		}
	}
}

// TestScrollAccept_RemovedEditingKeysKeepTheDraft is the end-to-end form: the
// draft must survive the keystroke through the real Update path.
func TestScrollAccept_RemovedEditingKeysKeepTheDraft(t *testing.T) {
	const draft = "keep this draft"
	for _, key := range removedEditingKeys {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		m.textarea.SetValue(draft)

		_, _ = m.Update(tea.KeyMsg{Type: keyTypeForCtrl(t, key)})

		if got := m.textarea.Value(); got != draft {
			t.Fatalf("%q altered the draft: %q -> %q", key, draft, got)
		}
	}
}

// keyTypeForCtrl maps the ctrl key names under test to bubbletea key types.
func keyTypeForCtrl(t *testing.T, key string) tea.KeyType {
	t.Helper()
	switch key {
	case "ctrl+u":
		return tea.KeyCtrlU
	case "ctrl+k":
		return tea.KeyCtrlK
	case "ctrl+w":
		return tea.KeyCtrlW
	}
	t.Fatalf("unmapped key %q", key)
	return 0
}

// TestScrollAccept_CtrlDDoesNotQuit locks the removal of ctrl+d. It sat directly
// beside ctrl+u's half-page-scroll, so a user who found ctrl+u and reflexively
// reached for ctrl+d exited mivia. Quitting is ctrl+c, /exit, exit or quit.
func TestScrollAccept_CtrlDDoesNotQuit(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusComposer)
	m.textarea.SetValue("half a question")

	_, _, cmds := m.handleChatKey("ctrl+d", false)
	if len(cmds) != 0 {
		t.Fatalf("ctrl+d must not emit a command (it used to quit), got %d", len(cmds))
	}
	if got := m.textarea.Value(); got != "half a question" {
		t.Fatalf("ctrl+d altered the draft: %q", got)
	}
}

// TestScrollAccept_HomeGoesToTopOfTranscript gives home a meaning. routeFocusKey
// promotes it to the transcript and consumes it, but nothing handled it and the
// viewport binds no home key - so it blurred the composer, scrolled nothing, and
// gave no feedback.
func TestScrollAccept_HomeGoesToTopOfTranscript(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	if m.viewport.YOffset == 0 {
		t.Fatal("precondition: transcript must start scrolled down")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})

	if m.viewport.YOffset != 0 {
		t.Fatalf("home must scroll to the top of the transcript, YOffset=%d", m.viewport.YOffset)
	}
	if m.followOutput {
		t.Fatal("home must disengage follow-mode: the user is reading history")
	}
}

// TestScrollAccept_ComposerShowsFocusWhileWaiting locks the state that was
// invisible exactly when it mattered. composer.go tested `waiting` before
// `focused`, so a user who paged into scrollback mid-turn saw an identical
// border while the textarea silently ignored every keystroke.
func TestScrollAccept_ComposerShowsFocusWhileWaiting(t *testing.T) {
	const width = 80
	focused := renderComposer("draft", width, true, 0, true, "", false)
	blurred := renderComposer("draft", width, true, 0, false, "", false)
	if focused == blurred {
		t.Fatal("composer looks identical focused and blurred while waiting: " +
			"the user cannot tell that typing is being ignored")
	}
	if strings.TrimSpace(stripANSI(focused)) == "" {
		t.Fatal("composer rendered empty")
	}
}

// TestToolPanelFocusEnablesExpand locks a dead affordance. focusLiveToolStrip set
// toolPanel.Focused without calling setFocus, so m.focus stayed focusComposer and
// the expand path - which requires focus != focusComposer - could never run. The
// panel advertised "enter/space expand" and Enter sent the draft instead.
func TestToolPanelFocusEnablesExpand(t *testing.T) {
	m := tallScrollModel(t, 6, 20)
	m.toolRows = []toolRow{
		{ToolCallID: "c1", Name: "read_file", Status: "completed", Done: true, Start: time.Now()},
		{ToolCallID: "c2", Name: "write_file", Status: "completed", Done: true, Start: time.Now()},
	}

	if !m.focusLiveToolStrip(false) {
		t.Fatal("focusLiveToolStrip must succeed with tool rows present")
	}
	if m.focus == focusComposer {
		t.Fatal("focusing the tool strip must move focus off the composer, " +
			"or the expand path is unreachable")
	}

	sel := m.toolPanel.Selected
	if sel < 0 || sel >= len(m.toolRows) {
		t.Fatalf("no tool row selected: %d", sel)
	}
	if m.toolRows[sel].Expanded {
		t.Fatal("precondition: row must start collapsed")
	}

	if _, _, _ = m.handleChatEnter(false); !m.toolRows[sel].Expanded {
		t.Fatal("enter must expand the selected tool row")
	}
}

// TestSlashHelpMatchesRealBindings keeps /help honest. It documented Ctrl+D as
// quit (now removed) and never mentioned a single scroll key, so a keyboard-only
// user had no way to learn how to read history.
func TestSlashHelpMatchesRealBindings(t *testing.T) {
	for _, gone := range []string{"Ctrl+D", "Ctrl+U", "Ctrl+K", "Ctrl+W"} {
		if strings.Contains(slashHelpMD, gone) {
			t.Errorf("/help still documents removed binding %s", gone)
		}
	}
	for _, want := range []string{"PgUp", "Home", "End", "Ctrl+R"} {
		if !strings.Contains(slashHelpMD, want) {
			t.Errorf("/help does not document %s", want)
		}
	}
}

// TestToolPanelHintOnlyClaimsWorkingKeys - nothing moves toolPanel.Selected on
// an arrow key, so the panel must not advertise it.
func TestToolPanelHintOnlyClaimsWorkingKeys(t *testing.T) {
	st := toolPanelState{Focused: true, Selected: 0}
	rows := []toolRow{{ToolCallID: "c1", Name: "read_file", Status: "completed", Done: true, Start: time.Now()}}
	rendered, _, _ := renderToolPanelWindow(rows, 80, time.Now(), st, 0, brandPhase(0), 4, 0)
	out := stripANSI(rendered)
	if strings.Contains(out, "↑↓ select") {
		t.Error("tool panel advertises arrow-key selection, which no handler implements")
	}
}

// TestRunDashboardRendersHeldRuns locks a warning that could never appear.
// renderRunLine reads HeldByAnotherExecutor, but renderPanel's deep copy omitted
// the field, so it was always false in the copy that gets rendered - a run held by
// another process looked resumable, and /resume then refused it for no visible reason.
func TestRunDashboardRendersHeldRuns(t *testing.T) {
	d := newRunDashboard()
	d.handleEvent(ledger.LifecycleEvent{RunID: "run-held", Kind: "run_created"})
	d.mu.Lock()
	d.runs["run-held"].HeldByAnotherExecutor = true
	d.mu.Unlock()
	d.toggleOpen()

	out := stripANSI(d.renderPanel(100))
	if out == "" {
		t.Fatal("precondition: panel must render")
	}
	if !strings.Contains(out, "held by another process") {
		t.Fatalf("a held run must be marked in the dashboard; got:\n%s", out)
	}
}
