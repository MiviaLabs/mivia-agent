package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

func findRegion(t *testing.T, s Screen, id sel.RegionID) (sel.Rect, bool) {
	t.Helper()
	for _, r := range s.SelectionRegions() {
		if r.ID == id {
			return (*r.Handle).SelectionRect(), true
		}
	}
	return sel.Rect{}, false
}

func TestSelectionRegionsTranscriptMatchesGeometry(t *testing.T) {
	s := sized(t, 0)
	_ = s.View() // View injects the rects; SelectionRegions reports them fresh
	tr, ok := findRegion(t, s, sel.RegionTranscript)
	if !ok {
		t.Fatal("a plain conversation screen must offer a transcript region")
	}
	wantY := 1 + s.topbar.Height() + 1 // gutter row + topbar rows + margin
	if tr.MinX != 1 || tr.MinY != wantY {
		t.Fatalf("transcript origin wrong: %+v (want x=1 y=%d)", tr, wantY)
	}
	if tr.MaxX-tr.MinX != s.chatWidth() || tr.MaxY-tr.MinY != s.transcriptHeight() {
		t.Fatalf("transcript size wrong: %+v (want %dx%d)", tr, s.chatWidth(), s.transcriptHeight())
	}
}

func TestSelectionRegionsComposerSitsAboveStatus(t *testing.T) {
	s := sized(t, 0)
	_ = s.View()
	cr, ok := findRegion(t, s, sel.RegionComposer)
	if !ok {
		t.Fatal("a composer-bearing screen must offer a composer region")
	}
	// The status row is the last content row, above the bottom gutter
	// row; the composer body ends above it (bottom padding row between).
	lastInputRow := s.height - 2 - s.composer.InputRowFromBottom()
	if cr.MaxY-1 != lastInputRow {
		t.Fatalf("composer bottom drift: rect=%+v lastInputRow=%d", cr, lastInputRow)
	}
	if cr.MinX < 1 {
		t.Fatalf("composer must start inside the left gutter: %+v", cr)
	}
}

func TestSelectionRegionsHiddenWhileDialogOpen(t *testing.T) {
	s := sized(t, 0)
	s.overlay = "help"
	if _, ok := findRegion(t, s, sel.RegionTranscript); ok {
		t.Fatal("an overlay covering the transcript area must remove its region")
	}
}

func TestSelectionRegionsEmbeddedNone(t *testing.T) {
	s := sized(t, 0)
	s.embedded = true
	if rs := s.SelectionRegions(); len(rs) != 0 {
		t.Fatalf("embedded thread screens report no regions in v1: %+v", rs)
	}
}

func TestViewInjectsRectsIntoComponents(t *testing.T) {
	s := sized(t, 0)
	_ = s.View() // View is a value receiver; Update is what syncs the live copy
	next, _ := s.Update(sel.CopyTextMsg{Text: "x"})
	s = next.(Screen)
	if got := s.transcript.SelectionRect(); got.Width() <= 0 || got.Height() <= 0 {
		t.Fatalf("Update must keep the transcript rect live: %+v", got)
	}
	if got := s.composer.SelectionRect(); got.Width() <= 0 || got.Height() <= 0 {
		t.Fatalf("Update must keep the composer rect live: %+v", got)
	}
}

// TestLiveCopyMutatesStackEntryState proves the pointer-handle contract
// end to end: a selection armed through SelectionRegions reaches the
// screen's own transcript model (the one View renders), not a copy.
func TestLiveCopyMutatesStackEntryState(t *testing.T) {
	s := sized(t, 1)
	ptr := &s
	regions := ptr.SelectionRegions()
	var found bool
	for _, r := range regions {
		if r.ID == sel.RegionTranscript {
			(*r.Handle).SetSelection(sel.Selection{Active: true})
			found = true
		}
	}
	if !found {
		t.Fatal("no transcript region on a screen with blocks")
	}
	if !s.transcript.HasSelection() {
		t.Fatal("handle must write through into the live transcript model")
	}
}

func TestCopyToastNoticesCharCount(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(sel.CopyTextMsg{Text: "hello"})
	s = next.(Screen)
	if v := s.statusline.View(fixedNow()); !strings.Contains(v, "copied 5 chars") {
		t.Fatalf("statusline must toast the copy attempt, got %q", v)
	}
}

// Boundary kills for the region geometry.

func TestSelectionRegionsTinySurfaceStaysInside(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 2, Height: 2})
	s = next.(Screen)
	// A two-by-two surface has no gutter and a bare bar: the body (just
	// the prompt) is drawn on the top row across both columns, and the
	// region must describe exactly that - never a cell past the surface.
	cr, ok := findRegion(t, s, sel.RegionComposer)
	if !ok {
		t.Fatal("the bare bar still draws a body on a tiny surface")
	}
	if cr.MinX != 0 || cr.MaxX != 2 || cr.MinY != 0 || cr.MaxY != 1 {
		t.Fatalf("composer region must be the drawn top row of a 2x2 surface: %+v", cr)
	}
}

func TestTranscriptRegionRightEdgeExclusive(t *testing.T) {
	s := sized(t, 1)
	tr, ok := findRegion(t, s, sel.RegionTranscript)
	if !ok {
		t.Fatal("no transcript region")
	}
	if tr.Contains(tr.MaxX, tr.MinY) || !tr.Contains(tr.MaxX-1, tr.MinY) {
		t.Fatalf("MaxX must be exclusive: %+v", tr)
	}
	if tr.Contains(tr.MinX, tr.MaxY) || !tr.Contains(tr.MinX, tr.MaxY-1) {
		t.Fatalf("MaxY must be exclusive: %+v", tr)
	}
}

func TestComposerRegionHeightTracksTextareaRows(t *testing.T) {
	s := sized(t, 0)
	_ = s.View()
	before := s.composer.Height() - s.composer.MenuRows()
	cr, ok := findRegion(t, s, sel.RegionComposer)
	if !ok {
		t.Fatal("no composer region")
	}
	if cr.Height() < 1 || cr.Height() > before {
		t.Fatalf("composer region height %d outside body rows (max %d)", cr.Height(), before)
	}
}

func TestContentOriginGutterBoundaries(t *testing.T) {
	cases := []struct {
		w, h     int
		wantX    int
		wantTopG int
	}{
		{80, 24, 1, 1}, // full gutter
		{2, 24, 0, 1},  // narrow: no side columns, top row stays
		{80, 2, 1, 0},  // short: gutter rows gone, columns stay
		{2, 2, 0, 0},   // tiny both ways: no gutter at all
		{3, 1, 1, 0},   // width exactly 3 (not "under three"): only height collapses
		{1, 3, 0, 1},   // height exactly 3 (not "under three"): only width collapses
	}
	for _, c := range cases {
		s := sized(t, 0)
		s.width, s.height = c.w, c.h
		x0, tg := s.contentOrigin()
		if x0 != c.wantX || tg != c.wantTopG {
			t.Fatalf("%dx%d: got origin (%d,%d), want (%d,%d)", c.w, c.h, x0, tg, c.wantX, c.wantTopG)
		}
	}
}

func TestSetComposerRectSkippedWhenHiddenOrEmbedded(t *testing.T) {
	s := sized(t, 0)
	s.setComposerRect() // armed first so the skip is observable
	if s.composer.SelectionRect().Width() <= 0 {
		t.Fatal("precondition: a visible composer gets a rect")
	}
	s2 := s
	s2.hideComposer = true
	s2.setComposerRect() // embedded||hide arm: no re-injection
	if got := s2.composer.SelectionRect(); got != s.composer.SelectionRect() {
		t.Fatalf("hidden composer must keep its previous rect untouched: %+v vs %+v", got, s.composer.SelectionRect())
	}
	if _, ok := findRegion(t, s2, sel.RegionComposer); ok {
		t.Fatal("a hidden composer reports no region")
	}
}

func TestContentOriginEmbeddedDropsTopGutter(t *testing.T) {
	s := sized(t, 0)
	s.embedded = true
	x0, tg := s.contentOrigin()
	if tg != 0 {
		t.Fatalf("an embedded screen must drop the top gutter, got tg=%d", tg)
	}
	if x0 != 1 {
		t.Fatalf("an embedded screen keeps its side gutter, got x0=%d", x0)
	}
}

func TestContentOriginUnmeasuredSurfaceCollapses(t *testing.T) {
	s := sized(t, 0)
	s.width, s.height = 0, 0
	x0, tg := s.contentOrigin()
	// Unmeasured (zero) dimensions are not "under three"; they collapse
	// both parts of the frame, matching gutter()'s join condition.
	if x0 != 0 || tg != 0 {
		t.Fatalf("an unmeasured surface collapses the gutter: (%d,%d)", x0, tg)
	}
}

func TestComposerRegionTracksBodyColumns(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	cr, ok := findRegion(t, s, sel.RegionComposer)
	if !ok {
		t.Fatal("a normal surface must report a composer region")
	}
	// The region covers exactly the textarea's inner columns: at least
	// one cell, never more than the chat column minus the frame insets.
	if cr.Width() < 1 || cr.MaxX > s.chatWidth()+1 {
		t.Fatalf("composer region columns out of bounds: %+v (chatWidth %d)", cr, s.chatWidth())
	}
}

func TestComposerRegionSingleColumnStillReports(t *testing.T) {
	// w == 1 is one drawable column, not the "cannot draw a single
	// cell" case the early-return guards against (w < 1): a <=
	// mutant on that guard would collapse this to an empty rect.
	// Width 3 is one content column inside the gutter; the bare bar
	// (no padding below minPaddedWidth) draws its body across it.
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 3, Height: 24})
	s = next.(Screen)
	cr := s.composerRegion()
	if cr.Width() != 1 {
		t.Fatalf("expected exactly one drawable column, got %+v", cr)
	}
}

func TestSetTranscriptRectEmbeddedSkipsInjection(t *testing.T) {
	s := sized(t, 0)
	s.setTranscriptRect() // armed first so the skip is observable
	before := s.transcript.SelectionRect()
	s2 := s
	s2.embedded = true
	s2.setTranscriptRect() // embedded arm: no re-injection
	if got := s2.transcript.SelectionRect(); got != before {
		t.Fatalf("embedded screen must keep its previous rect untouched: %+v vs %+v", got, before)
	}
}

func TestSetComposerRectEmbeddedSkipsInjection(t *testing.T) {
	s := sized(t, 0)
	s.setComposerRect() // armed first so the skip is observable
	before := s.composer.SelectionRect()
	s2 := s
	s2.embedded = true
	s2.setComposerRect() // embedded arm: no re-injection
	if got := s2.composer.SelectionRect(); got != before {
		t.Fatalf("embedded screen must keep its previous rect: %+v vs %+v", got, before)
	}
}

func TestContentOriginWidthExactlyThreeKeepsGutter(t *testing.T) {
	s := sized(t, 0)
	s.width, s.height = 3, 24
	x0, tg := s.contentOrigin()
	if x0 != 1 || tg != 1 {
		t.Fatalf("width exactly 3 keeps the gutter: (%d,%d)", x0, tg)
	}
	s.width, s.height = 80, 3
	x0, tg = s.contentOrigin()
	if x0 != 1 || tg != 1 {
		t.Fatalf("height exactly 3 keeps the gutter: (%d,%d)", x0, tg)
	}
}

func TestTranscriptRegionHiddenWhenPanelNarrowCoversIt(t *testing.T) {
	// A narrow, non-split panel does not change transcriptShown() or
	// the transcript rect: SelectionRegions() still reports it. The
	// panel's own click-through protection lives in handleClick, not
	// here, so this pins that SelectionRegions() does not duplicate it.
	s := sized(t, 1)
	s.panel.open = true // narrow (below breakpoint): the list covers the transcript
	if _, ok := findRegion(t, s, sel.RegionTranscript); !ok {
		t.Fatal("a narrow, unsplit panel must not hide the transcript region here")
	}
}

// TestComposerRegionCoversTheDrawnBody: the region must sit on the very
// cells View draws the body in - the row that carries the prompt and the
// column the prompt glyph starts at - so a drag over the typed text
// selects that text. An off-by-one row put the region on the bottom
// padding row (a press on the input row started no selection at all),
// and an off-by-one column copied " hell" for a drag over "hello".
func TestComposerRegionCoversTheDrawnBody(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	next, _ = s.Update(keyMsg("hello"))
	s = next.(Screen)
	rows := strings.Split(s.View(), "\n")
	s.syncSelectionRects()
	cr := s.composer.SelectionRect()

	promptRow, promptCol := -1, -1
	for y, row := range rows {
		if col := strings.Index(ansi.Strip(row), "> hello"); col >= 0 {
			promptRow, promptCol = y, col
			break
		}
	}
	if promptRow < 0 {
		t.Fatalf("precondition: the input row is drawn somewhere:\n%s", ansi.Strip(s.View()))
	}
	if cr.MinY != promptRow || cr.MaxY != promptRow+1 {
		t.Errorf("region rows %d..%d, want exactly the drawn input row %d", cr.MinY, cr.MaxY, promptRow)
	}
	if cr.MinX != promptCol {
		t.Errorf("region starts at column %d, want the prompt glyph's column %d", cr.MinX, promptCol)
	}
	if cr.MaxX != promptCol+s.chatWidth()-2*s.composer.InputColumnOffset() {
		t.Errorf("region ends at column %d, want the bar's inner right edge", cr.MaxX)
	}

	textCol := promptCol + 2 // after "> "
	from := sel.FromScreen(cr, textCol, promptRow)
	to := sel.FromScreen(cr, textCol+4, promptRow)
	s.composer.SetSelection(sel.Selection{Active: true, Anchor: from, Focus: to})
	if got := s.composer.SelectedText(); got != "hello" {
		t.Errorf("drag across the typed word selected %q, want hello", got)
	}
}
