package files

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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

func entries() []Entry {
	hunk := func(kind uievent.DiffLineKind, text string) uievent.DiffHunk {
		return uievent.DiffHunk{Header: "@@ -1 +1 @@", Lines: []uievent.DiffLine{{Kind: kind, Text: text}}}
	}
	return []Entry{
		NewEntry(uievent.Diff{Path: "a.go", Added: 3, Removed: 1, Hunks: []uievent.DiffHunk{hunk(uievent.DiffLineDel, "old a")}, After: []string{"package a"}}),
		NewEntry(uievent.Diff{Path: "b.go", Added: 2, Removed: 0, Hunks: []uievent.DiffHunk{hunk(uievent.DiffLineAdd, "new b")}, After: []string{"package b"}}),
		NewEntry(uievent.Diff{Path: "a.go", Added: 9, Removed: 2, Hunks: []uievent.DiffHunk{hunk(uievent.DiffLineAdd, "second edit hunk")}, After: []string{"package a", "// second edit"}}),
	}
}

// TestNewCollapsesRepeatedPathsToOneRowLatestWins: the tab answers the
// file's state, not its history.
func TestNewCollapsesRepeatedPathsToOneRowLatestWins(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	if len(m.entries) != 2 {
		t.Fatalf("got %d entries, want 2 (a.go collapsed to its latest)", len(m.entries))
	}
	if m.entries[0].Path != "a.go" || len(m.entries[0].Diff.After) != 2 {
		t.Errorf("a.go did not keep its latest edit: %+v", m.entries[0].Diff)
	}
}

// TestKindDerivedFromTheDiff: removals present means edited, none means
// created.
func TestKindDerivedFromTheDiff(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	if m.entries[0].Kind != KindEdited {
		t.Errorf("a.go kind %q, want edited (it has removals)", m.entries[0].Kind)
	}
	if m.entries[1].Kind != KindCreated {
		t.Errorf("b.go kind %q, want created (adds only)", m.entries[1].Kind)
	}
}

// TestWideViewShowsBothPanesAndFollowsSelection: at and above the wide
// breakpoint the tab is a split, the focused list pane carries the
// focus border's glyphs, and the right pane shows the selected file's
// diff lines.
func TestWideViewShowsBothPanesAndFollowsSelection(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	next, _ := m.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	m = next.(Model)
	view := m.View()
	for _, want := range []string{"a.go", "b.go", "edited", "created", "@@"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view missing %q:\n%s", want, view)
		}
	}
	if strings.Count(view, "╭") != 2 {
		t.Errorf("wide view frames %d panes, want 2:\n%s", strings.Count(view, "╭"), view)
	}

	// Moving the selection changes the right pane: down to b.go shows
	// its diff, not a.go's.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	if !strings.Contains(m.View(), "b.go") {
		t.Error("selection did not move to b.go")
	}
}

// TestToggleFlipsDiffAndSource: d shows the post-edit source, d again
// returns to the diff.
func TestToggleFlipsDiffAndSource(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	next, _ := m.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	m = next.(Model)
	if !strings.Contains(m.View(), "// second edit") {
		t.Errorf("source view does not show the after content:\n%s", m.View())
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	m = next.(Model)
	if !strings.Contains(m.View(), "@@") {
		t.Errorf("diff view did not return:\n%s", m.View())
	}
}

// TestNarrowViewCollapsesToListThenModal: below the wide breakpoint the
// tab is the list alone; Enter opens the content as a centered dialog;
// any key closes it back to the list.
func TestNarrowViewCollapsesToListThenModal(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = next.(Model)
	view := m.View()
	if !strings.Contains(view, "a.go") || strings.Contains(view, "@@") {
		t.Errorf("narrow view is not the bare list:\n%s", view)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if !m.modalOpen || !strings.Contains(m.View(), "@@") {
		t.Errorf("enter did not open the content modal:\n%s", m.View())
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.modalOpen {
		t.Error("escape did not close the modal")
	}
}

// TestEmptyStateNamesItself: no touched files is a stated empty state,
// not a blank pane.
func TestEmptyStateNamesItself(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	m = next.(Model)
	if !strings.Contains(m.View(), "no files touched yet") {
		t.Errorf("empty state missing:\n%s", m.View())
	}
}

// TestTabNextPopsBackToChat: ctrl+n from Files pops the router stack.
func TestTabNextPopsBackToChat(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, entries())
	next, _ := m.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	m = next.(Model)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+n produced no command")
	}
	msg := cmd()
	if _, ok := msg.(app.PopScreenMsg); !ok {
		t.Errorf("ctrl+n yielded %T, want PopScreenMsg", msg)
	}
}
