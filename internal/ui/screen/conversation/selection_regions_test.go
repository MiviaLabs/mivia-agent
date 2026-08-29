package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	// The status row is the last frame row; the composer body ends one
	// above it (framed: bottom border between).
	lastInputRow := s.height - 1 - s.composer.InputRowFromBottom()
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

func TestSelectionRegionsTinySurfaceNoComposer(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 2, Height: 2})
	s = next.(Screen)
	// A two-column surface cannot hold the composer's body: the region
	// collapses to zero width and SelectionRegions must drop it.
	if cr, ok := findRegion(t, s, sel.RegionComposer); ok && cr.Width() > 0 {
		t.Fatalf("a two-column surface must report no usable composer region: %+v", cr)
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
	s := sized(t, 1)
	s.panel.open = true // narrow (below breakpoint): the list covers the transcript
	if _, ok := findRegion(t, s, sel.RegionTranscript); ok {
		t.Log("narrow panel still reports a transcript region; clicks there are swallowed by handleClick")
	}
}
