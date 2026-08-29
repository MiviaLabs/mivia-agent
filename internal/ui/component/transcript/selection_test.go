package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

func sizedTranscript(t *testing.T) Model {
	t.Helper()
	m := New(theme.Theme{}, theme.TierTrueColor)
	m.SetSize(40, 5)
	return m
}

func pushProse(t *testing.T, m Model, text string) Model {
	t.Helper()
	next, _ := m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: text}})
	return next
}

func TestTranscriptSelectedTextAcrossRows(t *testing.T) {
	m := sizedTranscript(t)
	// One word per paragraph: prose rewraps, so a single short line per
	// block keeps every row predictable.
	m = pushProse(t, m, "alpha")
	m = pushProse(t, m, "bravo")
	m.SetSelectionRect(sel.Rect{MinX: 1, MinY: 2, MaxX: 41, MaxY: 7})

	from := sel.FromScreen(m.SelectionRect(), 1+0, 2+0) // row 0 ("alpha"), col 0
	to := sel.FromScreen(m.SelectionRect(), 1+3, 2+3)   // row 3 ("bravo"), col 3
	m.SetSelection(sel.Selection{Active: true, Anchor: from, Focus: to})

	rows := m.Rows()
	if len(rows) < 4 || !strings.Contains(rows[0], "alpha") || !strings.Contains(rows[3], "brav") {
		t.Fatalf("unexpected visible rows: %q", rows)
	}
	got := m.SelectedText()
	want := "alpha" + strings.Repeat(" ", 33) + "\n\n\nbrav"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTranscriptRowsPaintHighlight(t *testing.T) {
	m := sizedTranscript(t)
	m = pushProse(t, m, "hello world")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 4}})
	rows := m.Rows()
	if !strings.HasPrefix(rows[0], "\x1b[7m") || !strings.Contains(rows[0], "\x1b[27m") {
		t.Fatalf("highlight not painted into Rows(): %q", rows[0])
	}
}

func TestTranscriptScrollInvalidatesSelection(t *testing.T) {
	m := sizedTranscript(t)
	m = pushProse(t, m, strings.Repeat("line\n", 30))
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 2}})
	m = m.ScrollBy(-2)
	if m.HasSelection() {
		t.Fatal("scrolling must cancel a live selection")
	}
}

func TestTranscriptPushInvalidatesSelection(t *testing.T) {
	m := sizedTranscript(t)
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 2}})
	m = pushProse(t, m, "new block lands mid-drag")
	if m.HasSelection() {
		t.Fatal("a pushed block must cancel a live selection")
	}
}

func TestTranscriptClearSelectionDropsHighlight(t *testing.T) {
	m := sizedTranscript(t)
	m = pushProse(t, m, "plain text")
	m.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 3}})
	m.ClearSelection()
	if m.HasSelection() || m.SelectedText() != "" {
		t.Fatal("clear must drop both state and text")
	}
	for _, r := range m.Rows() {
		if strings.Contains(r, "\x1b[7m") {
			t.Fatalf("stale highlight after clear: %q", r)
		}
	}
}

func TestTranscriptSelectionReportsArmedAnchor(t *testing.T) {
	m := sizedTranscript(t)
	want := sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 3}}
	m.SetSelection(want)
	if got := m.Selection(); got != want {
		t.Fatalf("Selection() = %+v, want %+v", got, want)
	}
}

func TestTranscriptImplementsSelectable(t *testing.T) {
	var _ sel.Selectable = (*Model)(nil)
	m := sizedTranscript(t)
	m.SetSelectionRect(sel.Rect{MinX: 1, MinY: 3, MaxX: 41, MaxY: 8})
	if got := m.SelectionRect(); got.MinY != 3 || got.Width() != 40 {
		t.Fatalf("rect round-trip failed: %+v", got)
	}
}
