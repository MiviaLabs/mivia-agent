package composer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/charmbracelet/x/ansi"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

func testComposer() Model {
	m := New(theme.Theme{}, theme.TierTrueColor, 20)
	return m
}

func TestComposerSelectionRowsMatchTextareaView(t *testing.T) {
	m := testComposer()
	m.SetValue("hello brave world")
	rows := m.selectionRows()
	view := strings.Split(m.input.View(), "\n")
	if len(rows) != len(view) {
		t.Fatalf("row count mismatch: %d vs %d", len(rows), len(view))
	}
	for i := range rows {
		if strings.TrimRight(stripANSI(view[i]), " ") != rows[i] {
			t.Fatalf("row %d drift: view=%q sel=%q", i, stripANSI(view[i]), rows[i])
		}
	}
}

func TestComposerSelectedTextSingleRow(t *testing.T) {
	m := testComposer()
	m.SetValue("hello world")
	// Row 0 reads "> hello world": col 2..12 is "hello world".
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 2}, Focus: sel.Cell{Row: 0, Col: 12}})
	got := m.SelectedText()
	want := "hello world"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestComposerSelectedTextAcrossWrappedRows(t *testing.T) {
	m := testComposer()
	m.SetValue("hello brave world")
	// rows: "> hello brave", "> world". Select "brave" through "world".
	from := sel.Cell{Row: 0, Col: 8}
	to := sel.Cell{Row: 1, Col: 9}
	m.SetSelection(sel.Selection{Active: true, Anchor: from, Focus: to})
	got := m.SelectedText()
	want := "brave\n  world" // continuation rows carry the two-column indent
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestComposerEditInvalidatesSelection(t *testing.T) {
	m := testComposer()
	m.SetValue("keep me")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 3}})
	// Editing goes through the composer's Update path (a keystroke),
	// which mutates the textarea value.
	m2, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m2.HasSelection() {
		t.Fatal("a text change must cancel a live selection")
	}
	if m2.SelectedText() != "" {
		t.Fatal("stale selection must not copy")
	}
}

var _ = tea.KeyPressMsg{}

func TestComposerHighlightPaintsBody(t *testing.T) {
	m := testComposer()
	m.SetValue("abcdef")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 2}, Focus: sel.Cell{Row: 0, Col: 4}})
	out := m.View()
	if !strings.Contains(out, "\x1b[7m") {
		t.Fatalf("no reverse-video highlight in View: %q", out)
	}
}

func TestComposerImplementsSelectable(t *testing.T) {
	var _ sel.Selectable = (*Model)(nil)
	m := testComposer()
	m.SetSelectionRect(sel.Rect{MinX: 1, MinY: 5, MaxX: 21, MaxY: 7})
	if got := m.SelectionRect(); got.MinX != 1 || got.MaxY != 7 {
		t.Fatalf("rect round-trip failed: %+v", got)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// swallow sequence body
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Boundary kills for the composer's selection helpers.

func TestWrapLikeTextareaExactWidthFits(t *testing.T) {
	// "ab cd" is 5 wide: at width 6 it stays one row (the trailing space
	// of the wrap rule fits); at width 5 the word breaks to a new row.
	// A >= mutant would break the exact-width case too early.
	rows := wrapLikeTextarea("ab cd", 6)
	if len(rows) != 1 {
		t.Fatalf("width-6 line must stay on one row: %q", rows)
	}
	rows = wrapLikeTextarea("ab cd", 5)
	if len(rows) != 2 {
		t.Fatalf("one cell narrower must wrap to two rows: %q", rows)
	}
}

func TestRepeatSpacesBoundary(t *testing.T) {
	if repeatSpaces(0) != nil {
		t.Fatal("zero spaces must produce nil")
	}
	if string(repeatSpaces(1)) != " " {
		t.Fatal("one space must produce one space")
	}
}

func TestPromptCellsWidePromptTruncates(t *testing.T) {
	// A prompt wider than the cell budget truncates rather than
	// overflowing (pw >= w arm).
	got := promptCells(1, true)
	if ansi.StringWidth(got) != 1 {
		t.Fatalf("prompt cells must fit the budget: %q", got)
	}
	if promptCells(2, false) != "  " {
		t.Fatal("continuation indent is two blank cells")
	}
}

func TestComposerSelectionRowsPadToHeight(t *testing.T) {
	m := testComposer()
	// One short line in a two-row body: the pad arm (len(out) < h) must
	// fill the second row with blank prompt cells, not drop it.
	m.SetValue("hi")
	rows := m.selectionRows()
	if len(rows) != m.input.Height() {
		t.Fatalf("rows must pad to the textarea height %d, got %d", m.input.Height(), len(rows))
	}
}

func TestRaggedWrapZeroWidthReturnsSingleRow(t *testing.T) {
	// The width <= 0 guard returns the input as one row unchanged.
	got := raggedWrap([]rune("abc"), 0)
	if len(got) != 1 || string(got[0]) != "abc" {
		t.Fatalf("zero-width guard failed: %q", got)
	}
}

func TestComposerSelectedTextStaleValueEmpty(t *testing.T) {
	m := testComposer()
	m.SetValue("armed text")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 2}, Focus: sel.Cell{Row: 0, Col: 5}})
	if m.SelectedText() == "" {
		t.Fatal("freshly armed selection must copy")
	}
	// An edit invalidates via Update's value-comparison arm; a stale
	// selection must copy nothing even if state somehow survives.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if next.HasSelection() || next.SelectedText() != "" {
		t.Fatal("stale selection after an edit must copy nothing")
	}
}

func TestComposerSelectionRowsPadWhenShorterThanHeight(t *testing.T) {
	m := testComposer()
	m.SetValue("") // zero logical rows
	rows := m.selectionRows()
	if len(rows) != m.input.Height() {
		t.Fatalf("empty input pads to height %d, got %d", m.input.Height(), len(rows))
	}
	for _, r := range rows {
		if ansi.StringWidth(r) < promptWidth {
			t.Fatalf("padded row lost its prompt cells: %q", r)
		}
	}
}

func TestComposerSelectedTextStaleValueEmptyB(t *testing.T) {
	m := testComposer()
	m.SetValue("armed text")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 2}, Focus: sel.Cell{Row: 0, Col: 5}})
	// Force the stale-value arm: SelectedText guards on selValue.
	m.selValue = "different value"
	if m.SelectedText() != "" {
		t.Fatal("a selection armed against a different value must copy nothing")
	}
}
