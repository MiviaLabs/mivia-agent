package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func keyMsg(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

func TestUpdateIgnoresNonKeyMsg(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a", "b"})
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Error("expected no Cmd for a non-key Msg")
	}
	if got, _ := next.Selected(); got != "a" {
		t.Errorf("got %q, want the cursor unchanged by a non-key Msg", got)
	}
}

func TestSelectedOnEmptyList(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, nil)
	if _, ok := m.Selected(); ok {
		t.Error("expected no selection on an empty list")
	}
}

func TestCursorMovement(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a", "b", "c"})
	m, _ = m.Update(downKey())
	if got, _ := m.Selected(); got != "b" {
		t.Fatalf("got %q, want \"b\" after one down", got)
	}
	m, _ = m.Update(downKey())
	if got, _ := m.Selected(); got != "c" {
		t.Fatalf("got %q, want \"c\" after two downs", got)
	}
	m, _ = m.Update(downKey()) // at bottom: must not go out of bounds
	if got, _ := m.Selected(); got != "c" {
		t.Fatalf("got %q, want to stay at \"c\" past the end", got)
	}
	m, _ = m.Update(upKey())
	if got, _ := m.Selected(); got != "b" {
		t.Fatalf("got %q, want \"b\" after one up", got)
	}
}

func TestFilterNarrowsAndResetsCursor(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"apple", "banana", "avocado"})
	m, _ = m.Update(downKey()) // cursor -> banana
	m, _ = m.Update(keyMsg("a"))
	m, _ = m.Update(keyMsg("p")) // filter "ap": only "apple" matches (not "banana" or "avocado")
	if got, ok := m.Selected(); !ok || got != "apple" {
		t.Fatalf("got %q ok=%v, want cursor reset to the sole match \"apple\"", got, ok)
	}
	got := m.View()
	if strings.Contains(got, "banana") || strings.Contains(got, "avocado") {
		t.Errorf("expected only \"apple\" to survive the \"ap\" filter: %q", got)
	}
	if !strings.Contains(got, "/ap") {
		t.Errorf("expected the active filter shown, got %q", got)
	}
}

func TestBackspaceNarrowsFilterBack(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"apple", "banana"})
	m, _ = m.Update(keyMsg("b"))
	if _, ok := m.Selected(); !ok {
		t.Fatal("expected a match for filter \"b\"")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got, _ := m.Selected(); got != "apple" {
		t.Fatalf("got %q, want the filter cleared back to showing all items", got)
	}
}

func TestBackspaceHandlesMultiByteRune(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"café", "coffee", "漢字"})
	// Type multi-byte runes into the filter
	m, _ = m.Update(tea.KeyPressMsg{Text: "漢", Code: '漢'})
	m, _ = m.Update(tea.KeyPressMsg{Text: "字", Code: '字'})
	if m.filter != "漢字" {
		t.Fatalf("got filter %q, want \"漢字\"", m.filter)
	}
	if got, ok := m.Selected(); !ok || got != "漢字" {
		t.Fatalf("got %q ok=%v, want \"漢字\"", got, ok)
	}

	// Backspace once: removes '字', keeps '漢' intact without corrupting UTF-8
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.filter != "漢" {
		t.Fatalf("got filter %q, want \"漢\" after one backspace", m.filter)
	}

	// Backspace again: removes '漢', empty filter
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.filter != "" {
		t.Fatalf("got filter %q, want empty filter after second backspace", m.filter)
	}
}

func TestEnterEmitsSelectMsg(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a", "b"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a SelectMsg Cmd on enter")
	}
	msg, ok := cmd().(SelectMsg)
	if !ok || msg.Item != "a" {
		t.Errorf("got %+v ok=%v, want SelectMsg{Item: \"a\"}", msg, ok)
	}
}

// TestRebindKeepsFilterAndClampsCursor: a live-updating list refreshes
// its rows through Rebind, so an in-progress filter survives and the
// cursor never points past the re-filtered list.
func TestRebindKeepsFilterAndClampsCursor(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a1", "b1", "b2"})
	m, _ = m.Update(keyMsg("b"))
	m, _ = m.Update(downKey()) // cursor on "b2"
	if m.Filter() != "b" {
		t.Fatalf("precondition: filter %q, want \"b\"", m.Filter())
	}
	m.Rebind([]string{"a1", "b1", "b2", "b3"})
	if m.Filter() != "b" {
		t.Errorf("rebind reset the filter to %q; a live update must not wipe it", m.Filter())
	}
	if got, ok := m.Selected(); !ok || got != "b2" {
		t.Errorf("after rebind selected %q ok=%v, want the clamped cursor still on \"b2\"", got, ok)
	}
	// A rebind that shrinks the visible list below the cursor clamps it.
	m.Rebind([]string{"a1"})
	if m.Filter() != "b" {
		t.Fatalf("rebind must keep the filter, got %q", m.Filter())
	}
	if got, ok := m.Selected(); ok && got != "a1" {
		t.Errorf("cursor not clamped: selected %q, want the sole row or none", got)
	}
}

// TestRebindHoldsCursorOnUnfilteredList: with no filter, row indexes are
// stable across a rebind, so a caller can hold a selection by index.
func TestRebindHoldsCursorOnUnfilteredList(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a", "b"})
	m, _ = m.Update(downKey())
	m.Rebind([]string{"a", "b", "c"})
	if got, _ := m.Selected(); got != "b" {
		t.Errorf("rebind moved the selection to %q, want \"b\" held", got)
	}
	if m.CursorRow() != 1 {
		t.Errorf("CursorRow = %d, want 1", m.CursorRow())
	}
}

func TestEscEmitsCancelMsg(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"a"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a CancelMsg Cmd on esc")
	}
	if _, ok := cmd().(CancelMsg); !ok {
		t.Errorf("got %T, want CancelMsg", cmd())
	}
}

func downKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }
func upKey() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp} }

// TestClearFilterDropsItAndResetsTheCursor: dismissing surfaces that
// own a picker clear the filter so it cannot resurface later as an
// unexplained short list.
func TestClearFilterDropsItAndResetsTheCursor(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"apple", "banana", "avocado"})
	m, _ = m.Update(keyMsg("a"))
	m, _ = m.Update(downKey())
	if m.Filter() != "a" {
		t.Fatalf("precondition: filter %q, want \"a\"", m.Filter())
	}
	m.ClearFilter()
	if m.Filter() != "" {
		t.Errorf("filter survived ClearFilter: %q", m.Filter())
	}
	if got, _ := m.Selected(); got != "apple" {
		t.Errorf("cursor not reset, selected %q", got)
	}
}
