package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// editingKeys are the readline kill/delete bindings the composer honours:
// ctrl+u delete-before-cursor, ctrl+k delete-after-cursor, ctrl+w
// delete-word-backward. They were once swallowed in every focus to mask a
// focus bug - the viewport bound ctrl+u/ctrl+d to half-page scrolls, so one
// key edited or scrolled depending on pane. The INV-TUI-16 focus gate fixed
// the routing, and the viewport keymap no longer aliases any destructive
// editing key, so the standard editing keys are restored to the composer.
var editingKeys = []string{"ctrl+u", "ctrl+k", "ctrl+w"}

// TestScrollAccept_EditingKeysEditTheDraft asserts the restored keys perform
// their readline edits through the real Update path.
func TestScrollAccept_EditingKeysEditTheDraft(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		// SetValue leaves the cursor at end of line.
		{"ctrl+u", ""},            // delete before cursor: whole line gone
		{"ctrl+w", "keep this "},  // delete word backward
		{"ctrl+k", "keep this d"}, // after CursorStart+2 words… set below
	}
	for _, tc := range cases {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		m.textarea.SetValue("keep this draft")
		if tc.key == "ctrl+k" {
			// Put the cursor after "keep this d" so delete-after-cursor
			// leaves a visible prefix.
			m.textarea.CursorStart()
			for i := 0; i < len("keep this d"); i++ {
				_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
			}
		}

		_, _ = m.Update(tea.KeyMsg{Type: keyTypeForCtrl(t, tc.key)})

		if got := m.textarea.Value(); got != tc.want {
			t.Fatalf("%q: draft %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestScrollAccept_DestructiveKeysNeverScroll asserts the other half of the
// restoration: with scrollback focused, the editing keys neither scroll the
// transcript (viewport ctrl+u/ctrl+d aliases are stripped) nor touch the
// blurred draft. A destructive editing key must never have a scroll meaning.
func TestScrollAccept_DestructiveKeysNeverScroll(t *testing.T) {
	for _, key := range append(append([]string{}, editingKeys...), "ctrl+d") {
		m := tallScrollModel(t, 6, 200)
		m.setFocus(focusScrollback)
		m.textarea.SetValue("keep this draft")
		// Mid-scroll with room in both directions: at the bottom a
		// half-page-down no-ops and the assertion proves nothing.
		mid := (m.viewport.TotalLineCount() - m.viewport.Height) / 2
		m.viewport.SetYOffset(mid)
		before := m.viewport.YOffset
		if before <= 0 || m.viewport.AtBottom() {
			t.Fatalf("precondition: viewport must start mid-scroll (YOffset=%d, atBottom=%v)", before, m.viewport.AtBottom())
		}

		_, _ = m.Update(tea.KeyMsg{Type: keyTypeForCtrl(t, key)})

		if m.viewport.YOffset != before {
			t.Fatalf("%q scrolled the transcript: %d -> %d", key, before, m.viewport.YOffset)
		}
		if got := m.textarea.Value(); got != "keep this draft" {
			t.Fatalf("%q altered the blurred draft: %q", key, got)
		}
	}
}

// TestScrollAccept_CtrlArrowWordMotion asserts ctrl+←/→ move by words in the
// composer. bubbletea delivers CSI 1;5C/D as ctrl+right/ctrl+left; bubbles
// only binds the Emacs alt-forms by default, so the Windows/Linux convention
// was dead until the keymap declared both.
func TestScrollAccept_CtrlArrowWordMotion(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusComposer)
	m.textarea.SetValue("hello world")
	m.textarea.CursorStart()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if got := m.textarea.LineInfo().ColumnOffset; got == 0 {
		t.Fatal("ctrl+right did not move the cursor: word motion unbound")
	}

	m.textarea.CursorEnd()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if got := m.textarea.LineInfo().ColumnOffset; got >= len("hello world") {
		t.Fatal("ctrl+left did not move the cursor: word motion unbound")
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
	case "ctrl+d":
		return tea.KeyCtrlD
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

// home's transcript meaning now lives in
// TestScrollAccept_HomeGoesToTopWhenScrollbackFocused (tui_home_end_test.go):
// it scrolls to the oldest message while the transcript owns focus, and is
// the composer's line-start key while the composer does.

// TestScrollAccept_ComposerShowsFocusWhileWaiting locks the state that was
// invisible exactly when it mattered. composer.go tested `waiting` before
// `focused`, so a user who paged into scrollback mid-turn saw an identical
// border while the textarea silently ignored every keystroke.
func TestScrollAccept_ComposerShowsFocusWhileWaiting(t *testing.T) {
	const width = 80
	focused := renderComposer("draft", width, true, 0, true, phaseThinking, "", false)
	blurred := renderComposer("draft", width, true, 0, false, phaseThinking, "", false)
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

// TestSlashHelpMatchesRealBindings keeps /help honest - against what the user
// actually sees. The previous version checked slashHelpMD, a string nothing
// rendered: the real /help dialog showed the classic REPL's keys (Ctrl+U kill
// line, Ctrl+D exit, Tab completion), all false in the TUI, while the test
// passed. This version asserts on the rendered dialog content itself.
func TestSlashHelpMatchesRealBindings(t *testing.T) {
	dlg := newHelpDialog(100)
	joined := stripANSI(strings.Join(dlg.lines, "\n"))
	// Keys the TUI does not implement (or that alias enter/tab at the byte
	// level and can never fire) must not be advertised.
	for _, gone := range []string{
		"Ctrl+M", "Ctrl-M", // 0x0D IS enter; a distinct ctrl+m can never arrive
		"Kill entire line", // REPL Ctrl+U meaning; swallowed in the TUI
		"Kill word",        // REPL Ctrl+W meaning; swallowed in the TUI
		"Ctrl-D", "Ctrl+D", // REPL exit; deliberately removed from the TUI
		"Command completion", // REPL Tab; TUI Tab cycles focus
	} {
		if strings.Contains(joined, gone) {
			t.Errorf("/help advertises %q, which the TUI does not implement", gone)
		}
	}
	// Keys the TUI really binds must be discoverable.
	for _, want := range []string{"PgUp", "Home", "End", "Ctrl+R", "Ctrl+Q", "Ctrl+Y", "Ctrl+A", "Ctrl+G", "Tab", "F2"} {
		if !strings.Contains(joined, want) {
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
