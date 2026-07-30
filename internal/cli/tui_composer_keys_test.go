package cli

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// composerRuneKeys are the bare runes bubbles' viewport.DefaultKeyMap binds to
// scroll actions: "u"/"d" half page, "b"/"f"/" " page, "k"/"j" line,
// "h"/"l" horizontal. Typing any of them must reach the composer, never the
// transcript, or follow-mode dies mid-turn and the answer renders off-screen.
var composerRuneKeys = []string{"u", "d", "b", "f", " ", "k", "j", "h", "l"}

// TestScrollAccept_TypingInComposerDoesNotScrollOrUnfollow locks the defect
// where every keystroke was forwarded to the transcript viewport as well as the
// textarea. The viewport has no focus concept, so typing a word containing "u"
// scrolled history up and latched followOutput off for the rest of the session.
func TestScrollAccept_TypingInComposerDoesNotScrollOrUnfollow(t *testing.T) {
	for _, key := range composerRuneKeys {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		if !m.followOutput {
			t.Fatal("precondition: follow on")
		}
		if m.viewport.YOffset == 0 {
			t.Fatal("precondition: transcript must be scrolled to a non-zero bottom")
		}

		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})

		// Assert the user-visible property, not a raw offset: layout() may
		// legitimately recompute viewport height during Update, which shifts
		// the bottom offset without scrolling away from the latest content.
		if !m.followOutput {
			t.Fatalf("typing %q disengaged follow-mode", key)
		}
		if !m.viewport.AtBottom() {
			t.Fatalf("typing %q scrolled the transcript away from latest (YOffset %d)", key, m.viewport.YOffset)
		}
	}
}

// TestScrollAccept_ComposerCursorKeysDoNotScrollTranscript covers the same
// defect for the arrow keys, which the composer uses to move its caret.
func TestScrollAccept_ComposerCursorKeysDoNotScrollTranscript(t *testing.T) {
	for name, kt := range map[string]tea.KeyType{"up": tea.KeyUp, "down": tea.KeyDown} {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)

		_, _ = m.Update(tea.KeyMsg{Type: kt})

		if !m.followOutput {
			t.Fatalf("%s disengaged follow-mode while composer focused", name)
		}
		if !m.viewport.AtBottom() {
			t.Fatalf("%s scrolled the transcript while composer focused (YOffset %d)", name, m.viewport.YOffset)
		}
	}
}

// TestScrollAccept_ViewportGateFollowsFocus pins the mechanism: the transcript
// consumes keys only while it owns focus. This covers left/right too, which
// bubbles binds to horizontal panning while the composer uses them as cursor
// keys - behaviour that cannot be observed through YOffset.
func TestScrollAccept_ViewportGateFollowsFocus(t *testing.T) {
	composerOwned := append([]string{"left", "right", "up", "down", "a", "r"}, composerRuneKeys...)
	for _, key := range composerOwned {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		_, skipViewport, _ := m.handleChatKey(key, false)
		if !skipViewport {
			t.Fatalf("key %q must not reach the transcript while the composer has focus", key)
		}
	}
	// Keys that route focus to the transcript must still reach it. home/end
	// are deliberately absent: they are the composer's line-start/line-end
	// keys, and where they do mean "transcript" (scrollback focus, or end on
	// an empty draft) mivia scrolls itself and swallows them — see
	// TestScrollAccept_HomeGoesToTopWhenScrollbackFocused.
	for _, key := range []string{"pgup", "pgdown"} {
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		_, skipViewport, _ := m.handleChatKey(key, false)
		if skipViewport {
			t.Fatalf("key %q must reach the transcript", key)
		}
	}
}

// TestScrollAccept_ScrollbackKeysStillScroll is the counterweight: the focus
// gate must not make the transcript unscrollable. PgUp routes focus to
// scrollback, and arrow keys must then drive the viewport.
func TestScrollAccept_ScrollbackKeysStillScroll(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	preOff := m.viewport.YOffset

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.YOffset >= preOff {
		t.Fatalf("pgup must scroll up from %d, got %d", preOff, m.viewport.YOffset)
	}
	if m.followOutput {
		t.Fatal("pgup must unfollow")
	}
	if m.focus != focusScrollback {
		t.Fatalf("pgup must route focus to scrollback, got %v", m.focus)
	}

	upOff := m.viewport.YOffset
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.viewport.YOffset <= upOff {
		t.Fatalf("down must scroll while scrollback focused: %d -> %d", upOff, m.viewport.YOffset)
	}
}

// TestScrollAccept_EmptyDashboardDoesNotSwallowArrowKeys locks a trap created by
// gating the transcript on focus. The run dashboard consumed up/down whenever
// isOpen() was true, but renderPanel draws nothing when there are no runs and the
// status summary stays empty - so ctrl+r on a machine with no orchestration runs
// changed nothing on screen while silently making the arrow keys dead for both
// the composer caret and the transcript, with no way to guess the cause.
func TestScrollAccept_EmptyDashboardDoesNotSwallowArrowKeys(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.runDash = newRunDashboard()
	m.runDash.toggleOpen() // open, but there are no runs so nothing renders
	if m.runDash.renderPanel(m.width) != "" {
		t.Fatal("precondition: an empty dashboard must render nothing")
	}

	for _, key := range []string{"up", "down"} {
		skipTextarea, skipViewport, _ := m.handleChatKey(key, false)
		if skipTextarea && skipViewport {
			t.Fatalf("%q is swallowed by an invisible dashboard: both the composer "+
				"and the transcript ignore it", key)
		}
	}
}

// TestScrollAccept_VisibleDashboardOwnsArrowKeys is the counterweight: when the
// panel is actually on screen — and the reading side has focus — it must still
// drive its cursor. While the composer has focus the caret keys stay with the
// draft (TestScrollAccept_VisibleDashboardLeavesComposerArrows).
func TestScrollAccept_VisibleDashboardOwnsArrowKeys(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.runDash = newRunDashboard()
	m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "run-1", Kind: "run_created"})
	m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "run-2", Kind: "run_created"})
	m.runDash.toggleOpen()
	if m.runDash.renderPanel(m.width) == "" {
		t.Fatal("precondition: a dashboard with runs must render")
	}
	m.setFocus(focusScrollback)

	skipTextarea, skipViewport, _ := m.handleChatKey("down", false)
	if !skipTextarea || !skipViewport {
		t.Fatal("a visible dashboard must own up/down for its cursor")
	}
}
