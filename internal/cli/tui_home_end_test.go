package cli

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// Home and End are line-editing keys first. routeFocusKey used to consume them
// unconditionally and hand them to the transcript, so a user editing a message
// could not reach the start or the end of their own line - the composer had no
// line-start/line-end key at all once ctrl+e was also taken by select mode.
// The transcript keeps them while it owns focus, and shift+home/shift+end
// reach it from anywhere.

func composerDraftModel(t *testing.T, draft string) *tuiModel {
	t.Helper()
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusComposer)
	m.textarea.SetValue(draft)
	return m
}

// TestScrollAccept_HomeMovesComposerCursor: home is line-start while typing.
func TestScrollAccept_HomeMovesComposerCursor(t *testing.T) {
	m := composerDraftModel(t, "hello world")
	if m.textarea.LineInfo().ColumnOffset == 0 {
		t.Fatal("precondition: cursor must start at end of line")
	}
	beforeOffset := m.viewport.YOffset

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})

	if got := m.textarea.LineInfo().ColumnOffset; got != 0 {
		t.Fatalf("home must move the composer cursor to line start, col=%d", got)
	}
	if m.focus != focusComposer {
		t.Fatalf("home must not blur the composer, focus=%v", m.focus)
	}
	if m.viewport.YOffset != beforeOffset {
		t.Fatalf("home must not scroll the transcript while composing: %d -> %d", beforeOffset, m.viewport.YOffset)
	}
}

// TestScrollAccept_EndMovesComposerCursor: end is line-end with a live draft.
func TestScrollAccept_EndMovesComposerCursor(t *testing.T) {
	m := composerDraftModel(t, "hello world")
	m.textarea.CursorStart()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})

	if got := m.textarea.LineInfo().ColumnOffset; got == 0 {
		t.Fatal("end must move the composer cursor to line end")
	}
	if m.focus != focusComposer {
		t.Fatalf("end must not blur the composer, focus=%v", m.focus)
	}
}

// TestScrollAccept_EndOnEmptyDraftJumpsToLatest keeps the jump-to-latest
// affordance the hint line advertises ("↓ latest"). With nothing typed there
// is no line to move within, so end keeps its reading meaning.
func TestScrollAccept_EndOnEmptyDraftJumpsToLatest(t *testing.T) {
	m := composerDraftModel(t, "")
	m.noteUserScrolledUp()
	m.viewport.SetYOffset(3)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})

	if !m.followOutput {
		t.Fatal("end on an empty draft must re-enable follow")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("end on an empty draft must jump to the latest message")
	}
}

// TestScrollAccept_HomeGoesToTopWhenScrollbackFocused: the reading meaning
// survives where it belongs - with the transcript focused.
func TestScrollAccept_HomeGoesToTopWhenScrollbackFocused(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(focusScrollback)
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

// TestScrollAccept_ShiftHomeEndReachTranscriptFromComposer gives the transcript
// extremes a keyboard route that does not disturb the draft. Note these are
// a bonus, not the only path: GNOME Terminal/VTE and Konsole bind shift+home
// and shift+end to their own scrollback and consume them before the app sees
// them, which is why tab+home and an empty-draft end still work.
func TestScrollAccept_ShiftHomeEndReachTranscriptFromComposer(t *testing.T) {
	m := composerDraftModel(t, "unfinished thought")
	m.viewport.GotoBottom()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftHome})
	if m.viewport.YOffset != 0 {
		t.Fatalf("shift+home must scroll the transcript to the top, YOffset=%d", m.viewport.YOffset)
	}
	if got := m.textarea.Value(); got != "unfinished thought" {
		t.Fatalf("shift+home altered the draft: %q", got)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftEnd})
	if !m.viewport.AtBottom() || !m.followOutput {
		t.Fatalf("shift+end must jump to latest (atBottom=%v follow=%v)", m.viewport.AtBottom(), m.followOutput)
	}
	if got := m.textarea.Value(); got != "unfinished thought" {
		t.Fatalf("shift+end altered the draft: %q", got)
	}
}

// TestScrollAccept_VisibleDashboardLeavesComposerArrows: a run dashboard drawn
// above the composer must not steal the caret keys from a multi-line draft.
// It owns up/down only while the transcript side has focus.
func TestScrollAccept_VisibleDashboardLeavesComposerArrows(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.runDash = newRunDashboard()
	m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "run-1", Kind: "run_created"})
	m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "run-2", Kind: "run_created"})
	m.runDash.toggleOpen()
	if m.runDash.renderPanel(m.width) == "" {
		t.Fatal("precondition: a dashboard with runs must render")
	}
	m.setFocus(focusComposer)

	skipTextarea, _, _ := m.handleChatKey("down", false)
	if skipTextarea {
		t.Fatal("a visible dashboard must not swallow the composer's caret keys")
	}
}
