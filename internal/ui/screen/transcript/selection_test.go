package transcript

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	conv "github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func pagerWithRows(t *testing.T) Screen {
	t.Helper()
	return sizedPager(t, 60, 20)
}

func TestPagerSelectionRectInsideGutter(t *testing.T) {
	s := pagerWithRows(t)
	r := s.SelectionRect()
	if r.MinX != 1 || r.MaxX != 1+contentWidth(60) {
		t.Fatalf("rect must sit inside the one-column gutter: %+v", r)
	}
	if r.Height() != s.contentHeight() {
		t.Fatalf("rect height %d != content height %d", r.Height(), s.contentHeight())
	}
}

func TestPagerSelectedTextAcrossRows(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 3}})
	got := s.SelectedText()
	want := ansi.Strip(s.rows[0]) + "\n" + ansi.Strip(s.rows[1])[:min(4, len(ansi.Strip(s.rows[1])))]
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPagerViewPaintsHighlight(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 4}})
	v := s.View()
	if !strings.Contains(v, "\x1b[7m") {
		t.Fatal("pager View must paint the selection highlight")
	}
}

func TestPagerScrollInvalidatesSelection(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 1}})
	next, _ := s.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	s = next.(Screen)
	if s.HasSelection() {
		t.Fatal("wheel scroll must cancel a live selection")
	}
}

func TestPagerCopyToastStatusLine(t *testing.T) {
	s := pagerWithRows(t)
	next, _ := s.Update(sel.CopyTextMsg{Text: "abc"})
	s = next.(Screen)
	if v := s.statusLine(); !strings.Contains(v, "copied 3 chars") {
		t.Fatalf("status line must toast the copy attempt: %q", v)
	}
}

func TestPagerHandoverHasNoRegion(t *testing.T) {
	s := pagerWithRows(t)
	s.mode = modeHandover
	if rs := s.SelectionRegions(); len(rs) != 0 {
		t.Fatalf("handover mode releases the mouse; no region allowed: %+v", rs)
	}
	if s.SelectedText() != "" {
		s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 2}})
		if s.SelectedText() != "" {
			t.Fatal("handover mode must not copy")
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Boundary kills for the pager's selection helpers.

func TestPagerSetSelectionCopiesOnWrite(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 2}})
	before := s // copy shares the armed selection
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 1}})
	if !before.HasSelection() || before.SelectedText() == "" {
		t.Fatal("a published copy must keep painting what it was rendered with")
	}
}

func TestPagerSelectedTextStopsAtLastVisibleRow(t *testing.T) {
	s := pagerWithRows(t)
	// The selection window is the visible contentHeight() rows; a focus
	// row past the end must not reach conversation rows below the fold.
	visible := s.contentHeight()
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 999, Col: 0}})
	got := s.SelectedText()
	lines := strings.Split(got, "\n")
	if len(lines) != visible {
		t.Fatalf("selection must cover exactly the visible rows (%d), got %d", visible, len(lines))
	}
	if lines[0] != ansi.Strip(s.rows[0]) {
		t.Fatalf("first copied line must be the first visible row: %q", lines[0])
	}
}

func TestPagerTinySurfaceHasNoRegion(t *testing.T) {
	s := sizedPager(t, 2, 2) // below the gutter threshold
	if rs := s.SelectionRegions(); len(rs) != 0 {
		t.Fatalf("a surface under 3x3 draws no gutter and reports no region: %+v", rs)
	}
}

func TestPagerSetSelectionCopiesOnWriteKeepsOldPaint(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 2}})
	old := s // shares the armed selection pointer
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 1}})
	if !old.HasSelection() || old.SelectedText() == "" {
		t.Fatal("a published copy must keep painting what it was rendered with")
	}
}

func TestPagerClearSelectionNilStateReadsEmpty(t *testing.T) {
	s := pagerWithRows(t)
	s.ClearSelection() // nil arm of Selection()
	if s.Selection().Active {
		t.Fatal("cleared selection must read inactive")
	}
}

func TestPagerSelectedTextInactiveEmpty(t *testing.T) {
	s := pagerWithRows(t)
	// HasSelection false arm of the SelectedText guard.
	if s.SelectedText() != "" {
		t.Fatal("no selection must copy nothing")
	}
}

func TestPagerRegionHeightMatchesContentWindow(t *testing.T) {
	s := sizedPager(t, 60, 20)
	r := s.SelectionRect()
	if r.Height() != s.contentHeight() {
		t.Fatalf("region height %d must equal content window %d", r.Height(), s.contentHeight())
	}
}

func TestPagerCopyToastCountsRunes(t *testing.T) {
	s := pagerWithRows(t)
	s.handleCopyToast("日本語") // 3 runes, multi-byte
	if s.notice != "copied 3 chars" {
		t.Fatalf("toast must count runes: %q", s.notice)
	}
}

func TestPagerRegionWidthMatchesContentWidth(t *testing.T) {
	s := sizedPager(t, 60, 20)
	r := s.SelectionRect()
	if r.Width() != contentWidth(60) {
		t.Fatalf("region width %d must equal contentWidth(60)=%d", r.Width(), contentWidth(60))
	}
}

func TestPagerSelectionRegionShortSurfaceEmpty(t *testing.T) {
	s := sizedPager(t, 60, 2)
	if r := s.SelectionRect(); r != (sel.Rect{}) {
		t.Fatalf("a two-row surface draws no status-safe window: %+v", r)
	}
}

func TestPagerSetSelectionSameValueKeepsPointer(t *testing.T) {
	s := pagerWithRows(t)
	shared := &sel.Selection{}
	s.selState = shared
	first := s.selState
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 2}})
	if s.selState == first {
		t.Fatal("a changed selection must copy-on-write to a fresh pointer")
	}
	before := s.selState
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 0, Col: 2}})
	if s.selState != before {
		t.Fatal("an identical SetSelection must keep the published pointer")
	}
}

func TestPagerSelectedTextInactiveEmptyB(t *testing.T) {
	s := pagerWithRows(t)
	// nil selState arm of Selection().
	if s.Selection().Active {
		t.Fatal("nil state reads inactive")
	}
}

func TestPagerRegionZeroHeightNoEntry(t *testing.T) {
	s := sizedPager(t, 60, 1) // contentHeight clamps to 1; height < 3 kills the rect
	if rs := s.SelectionRegions(); len(rs) != 0 {
		t.Fatalf("a one-row surface reports no region: %+v", rs)
	}
}

func TestPagerRegionWidthExactlyThreeKeepsGutter(t *testing.T) {
	s := sizedPager(t, 3, 20) // width exactly at the gutter threshold
	r := s.SelectionRect()
	if r.Width() != contentWidth(3) || r.Width() <= 0 {
		t.Fatalf("width 3 must keep a one-column region: %+v", r)
	}
}

func TestPagerRegionHeightExactlyThreeKeepsWindow(t *testing.T) {
	s := sizedPager(t, 60, 3) // height exactly at the threshold
	r := s.SelectionRect()
	if r.Height() != s.contentHeight() || r.Height() <= 0 {
		t.Fatalf("height 3 must keep a two-row window: %+v", r)
	}
}

func TestPagerSelectedTextHandoverModeEmpty(t *testing.T) {
	s := pagerWithRows(t)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 1, Col: 1}})
	s.mode = modeHandover // the second guard arm of SelectedText
	if s.SelectedText() != "" {
		t.Fatal("handover mode must not copy")
	}
}

func TestPagerSelectedTextShortConversationStopsAtEnd(t *testing.T) {
	// A conversation shorter than the window: the loop's `>=` bound on
	// len(s.rows) stops at the last row; a `>` mutant would index past it.
	s := sizedPager(t, 60, 30)
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 999, Col: 999}})
	got := s.SelectedText()
	lines := strings.Split(got, "\n")
	if len(lines) > len(s.rows) || len(lines) < 1 {
		t.Fatalf("copied rows %d out of range [1, %d]", len(lines), len(s.rows))
	}
	for i, l := range lines {
		if want := ansi.Strip(s.rows[i]); l != want {
			t.Fatalf("row %d drift: %q vs %q", i, l, want)
		}
	}
}

func TestPagerSelectedTextEmptyRowsCopiesNothing(t *testing.T) {
	s := sizedPager(t, 60, 20)
	s.conv = conv.New(loadTheme(t), theme.TierASCII) // empty conversation: no rows
	s.rebuild()
	if len(s.rows) > 1 {
		t.Fatalf("precondition: empty conversation yields at most one blank row, got %d", len(s.rows))
	}
	s.rows = nil
	s.SetSelection(sel.Selection{Active: true, Anchor: sel.Cell{Row: 0, Col: 0}, Focus: sel.Cell{Row: 5, Col: 5}})
	if got := s.SelectedText(); got != "" {
		t.Fatalf("no visible rows must copy nothing, got %q", got)
	}
}

func TestPagerRegionWidthExactlyThreeB(t *testing.T) {
	s := sizedPager(t, 3, 20)
	r := s.SelectionRect()
	if r.Width() <= 0 || r.MaxX-r.MinX != contentWidth(3) {
		t.Fatalf("width 3 keeps a positive region: %+v", r)
	}
}
