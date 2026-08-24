package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestPickerSliceImmutability(t *testing.T) {
	items := []string{"a", "b"}
	m := New(loadTheme(t), theme.TierASCII, items)
	items[0] = "MUTATED"
	if got, _ := m.Selected(); got == "MUTATED" {
		t.Error("New did not clone items slice; external mutation corrupted picker")
	}

	rebindItems := []string{"x", "y"}
	m.Rebind(rebindItems)
	rebindItems[0] = "MUTATED_REBIND"
	if got, _ := m.Selected(); got == "MUTATED_REBIND" {
		t.Error("Rebind did not clone items slice; external mutation corrupted picker")
	}
}

func TestMoveToClampsToVisibleListWhenFilterIsActive(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, []string{"apple", "avocado", "banana", "blueberry", "cherry"})
	m, _ = m.Update(keyMsg("b")) // Filter "b" -> visible: ["banana", "blueberry"] (len 2)
	m.MoveTo(4)                  // 4 is valid for items (5 items), but > len(visible) (2 items)
	if got, ok := m.Selected(); !ok || got != "blueberry" {
		t.Errorf("MoveTo(4) with filter \"b\" selected %q (ok=%v), want \"blueberry\"", got, ok)
	}
}

func TestNewGroupsRendersProviderHeaderAndStartsCursorOnModel(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2", "llama3.3"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	view := ansi.Strip(m.View())
	for _, want := range []string{"ollama", "openrouter", "llama3.2", "llama3.3", "gpt-4o"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if got, ok := m.Selected(); !ok || got != "llama3.2" {
		t.Errorf("initial selection %q (ok=%v), want \"llama3.2\"", got, ok)
	}
}

func TestEmptyProviderRendersFlatWithoutHeader(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "", Models: []string{"a", "b"}},
	})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "a") || !strings.Contains(view, "b") {
		t.Errorf("view missing items:\n%s", view)
	}
	// No header row should appear (the single anonymous group's
	// provider is empty).
	if strings.Contains(view, "\n\n") {
		t.Errorf("view has unexpected blank row for empty provider:\n%s", view)
	}
}

func TestEmptyGroupsAreDropped(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: nil},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
		{Provider: "broken", Models: []string{}},
	})
	view := ansi.Strip(m.View())
	if strings.Contains(view, "ollama") || strings.Contains(view, "broken") {
		t.Errorf("empty groups leaked into the view:\n%s", view)
	}
	if !strings.Contains(view, "openrouter") || !strings.Contains(view, "gpt-4o") {
		t.Errorf("non-empty group dropped:\n%s", view)
	}
}

func TestEnterOnHeaderIsNoOp(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	// The cursor starts on the first model item ("llama3.2"), not on
	// the leading "ollama" header.
	if got, ok := m.Selected(); !ok || got != "llama3.2" {
		t.Fatalf("precondition: selected %q ok=%v, want llama3.2", got, ok)
	}
	// Up should land on the "ollama" header; Selected becomes false.
	m, _ = m.Update(upKey())
	if _, ok := m.Selected(); ok {
		t.Fatal("after up from the first model the cursor should be on the header")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Enter on a header emitted a Cmd, want none (headers are non-selectable)")
	}
}

func TestDownFromFirstModelSkipsNothing(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	// The cursor starts on the first model item ("llama3.2"). One
	// down lands on the "openrouter" header row.
	m, _ = m.Update(downKey())
	if _, ok := m.Selected(); ok {
		t.Error("one down from the first model should land on the next group's header")
	}
	// One more down lands on "gpt-4o".
	m, _ = m.Update(downKey())
	if got, ok := m.Selected(); !ok || got != "gpt-4o" {
		t.Errorf("two downs from start selected %q (ok=%v), want \"gpt-4o\"", got, ok)
	}
}

func TestUpFromFirstHeaderStaysAtTop(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
	})
	m, _ = m.Update(upKey())
	if m.CursorRow() != 0 {
		t.Errorf("up at the top moved cursor to %d, want 0", m.CursorRow())
	}
}

func TestFilterByProviderKeepsAllModelsInThatGroup(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2", "llama3.3"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	m, _ = m.Update(keyMsg("oll"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "llama3.2") || !strings.Contains(view, "llama3.3") {
		t.Errorf("filter \"oll\" should keep every ollama model:\n%s", view)
	}
	if strings.Contains(view, "gpt-4o") {
		t.Errorf("openrouter model leaked past the ollama filter:\n%s", view)
	}
}

func TestFilterByModelDropsOtherGroups(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2", "llama3.3"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	m, _ = m.Update(keyMsg("gpt"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "gpt-4o") {
		t.Errorf("filter \"gpt\" should keep gpt-4o:\n%s", view)
	}
	if strings.Contains(view, "llama3.2") || strings.Contains(view, "llama3.3") {
		t.Errorf("ollama models leaked past the gpt filter:\n%s", view)
	}
}

func TestFilterThatMatchesNothingProducesNoRows(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
	})
	m, _ = m.Update(keyMsg("zzz"))
	if got, _ := m.Selected(); got != "" {
		t.Errorf("no-match filter selected %q, want none", got)
	}
}

func TestClearFilterAfterGroupedFilterRestoresFullList(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2", "llama3.3"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	m, _ = m.Update(keyMsg("oll"))
	m.ClearFilter()
	view := ansi.Strip(m.View())
	for _, want := range []string{"ollama", "openrouter", "llama3.2", "gpt-4o"} {
		if !strings.Contains(view, want) {
			t.Errorf("after ClearFilter view missing %q:\n%s", want, view)
		}
	}
}

func TestRebindGroupsKeepsFilterAndClampsCursor(t *testing.T) {
	m := NewGroups(loadTheme(t), theme.TierASCII, []Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
		{Provider: "openrouter", Models: []string{"gpt-4o"}},
	})
	m, _ = m.Update(keyMsg("gpt"))
	m.RebindGroups([]Group{
		{Provider: "ollama", Models: []string{"llama3.2"}},
		{Provider: "openrouter", Models: []string{"gpt-4o-mini", "gpt-4o"}},
	})
	if m.Filter() != "gpt" {
		t.Errorf("rebind reset the filter, want it preserved")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "gpt-4o") || !strings.Contains(view, "gpt-4o-mini") {
		t.Errorf("rebind with filter kept did not rebuild the grouped list:\n%s", view)
	}
}
