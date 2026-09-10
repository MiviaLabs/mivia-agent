package render

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// plain strips styling so assertions test layout, not colour.
func plain(s string) string { return ansi.Strip(s) }

func TestHeaderPlacesMetaAndStateInline(t *testing.T) {
	th := loadTheme(t)
	const width = 60
	got := Header(th, theme.TierASCII, width, HeaderSpec{
		Marker: "v", Label: "edit", Detail: "main.go", Meta: "+4 -1", State: "ok",
	})
	p := plain(got)
	// Ragged-right: the row ends right after the state, not padded out
	// to width, so metadata reads close to the content it describes.
	if w := lipgloss.Width(got); w >= width {
		t.Errorf("got width %d, want it well short of %d (no fill padding)", w, width)
	}
	if !strings.HasSuffix(p, "ok") {
		t.Errorf("got %q, want the state as the last thing on the row", got)
	}
	if !strings.HasPrefix(p, "v edit") {
		t.Errorf("got %q, want the marker and label at the left", got)
	}
	if want := "main.go  +4 -1  ok"; !strings.HasSuffix(p, want) {
		t.Errorf("got %q, want detail and right columns joined with a fixed gap: %q", p, want)
	}
}

func TestHeaderWithoutRightColumns(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierASCII, 40, HeaderSpec{Marker: "v", Label: "notice", Detail: "hello"})
	// Nothing to right-align, so no padding run is added.
	if strings.HasSuffix(plain(got), " ") {
		t.Errorf("got %q, want no trailing padding when meta and state are absent", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("got %q, want the detail present", got)
	}
}

func TestHeaderStateWithoutMeta(t *testing.T) {
	th := loadTheme(t)
	const width = 40
	got := Header(th, theme.TierASCII, width, HeaderSpec{
		Marker: "v", Label: "run", State: "running", StateRole: theme.RoleInfo,
	})
	if w := lipgloss.Width(got); w >= width {
		t.Errorf("got width %d, want it well short of %d (no fill padding)", w, width)
	}
	if !strings.HasSuffix(plain(got), "running") {
		t.Errorf("got %q, want the state as the last thing on the row", got)
	}
}

func TestHeaderMetaWithoutState(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierASCII, 40, HeaderSpec{Marker: "v", Label: "plan", Meta: "2 of 4"})
	if !strings.HasSuffix(plain(got), "2 of 4") {
		t.Errorf("got %q, want the meta right-aligned", got)
	}
}

// TestHeaderClipsDetailNotState pins the priority: when the columns
// cannot all fit, the detail gives way. The state carries meaning and
// the label identifies the block, so neither may be cut.
func TestHeaderClipsDetailNotState(t *testing.T) {
	th := loadTheme(t)
	const width = 40
	long := strings.Repeat("verylongpath/", 12)
	got := Header(th, theme.TierASCII, width, HeaderSpec{
		Marker: "v", Label: "edit", Detail: long, Meta: "+4 -1", State: "ok",
	})
	if w := lipgloss.Width(got); w > width {
		t.Errorf("got width %d, want at most %d:\n%q", w, width, got)
	}
	if !strings.Contains(plain(got), uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q", got, uikitconfig.ClipMarker)
	}
	if !strings.HasSuffix(plain(got), "ok") {
		t.Errorf("got %q, want the state preserved", got)
	}
	if !strings.HasPrefix(plain(got), "v edit") {
		t.Errorf("got %q, want the label preserved", got)
	}
}

// TestHeaderClipsDetailNotOutcomeGlyphOrState pins the same clip
// priority TestHeaderClipsDetailNotState pins, for a tool block's own
// Marker content: C3 puts the call's outcome glyph ("x" for failed) in
// the Marker column instead of the generic collapse arrow, and this
// contract must not care which one it is. A failed row must stay
// exactly one row, the detail gives way first, and neither the glyph
// nor the "failed" word may be dropped.
func TestHeaderClipsDetailNotOutcomeGlyphOrState(t *testing.T) {
	th := loadTheme(t)
	const width = 40
	long := strings.Repeat("verylongpath/", 12)
	got := Header(th, theme.TierASCII, width, HeaderSpec{
		Marker: "x", Label: "run_command", Detail: long, Meta: "4.1s", State: "failed", StateRole: theme.RoleDanger,
	})
	p := plain(got)
	if strings.Count(p, "\n") != 0 {
		t.Fatalf("got %q, want exactly one row", p)
	}
	if w := lipgloss.Width(got); w > width {
		t.Errorf("got width %d, want at most %d:\n%q", w, width, got)
	}
	if !strings.HasPrefix(p, "x run_command") {
		t.Errorf("got %q, want the outcome glyph and label preserved", p)
	}
	if !strings.Contains(p, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker: the detail must give way first", p)
	}
	if !strings.HasSuffix(p, "failed") {
		t.Errorf("got %q, want the state preserved as the last thing on the row", p)
	}
}

// TestHeaderUnclippableStillRenders covers a width so small that even
// clipping the detail cannot help. It must degrade, never panic or
// produce a negative-width pad.
func TestHeaderUnclippableStillRenders(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierASCII, 8, HeaderSpec{
		Marker: "v", Label: "run_command", Detail: "x", Meta: "1234ms", State: "failed",
	})
	if got == "" {
		t.Fatal("expected some output at a hostile width")
	}
	if !strings.Contains(got, "failed") {
		t.Errorf("got %q, want the state word retained", got)
	}
}

func TestHeaderNoDetailToClip(t *testing.T) {
	th := loadTheme(t)
	// No detail: there is nothing to give up, so the label must survive
	// rather than be trimmed to make room.
	got := Header(th, theme.TierASCII, 30, HeaderSpec{
		Marker: "v", Label: "run_command", Meta: "1234ms", State: "failed",
	})
	if !strings.Contains(got, "run_command") {
		t.Errorf("got %q, want the label intact", got)
	}
}

// TestHeaderUnknownWidth covers width <= 0, which means "not measured
// yet". It must not invent a column layout.
func TestHeaderUnknownWidth(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierASCII, 0, HeaderSpec{
		Marker: "v", Label: "edit", Detail: "main.go", Meta: "+1", State: "ok",
	})
	for _, want := range []string{"v edit", "main.go", "+1", "ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, missing %q", got, want)
		}
	}
	if strings.Contains(plain(got), "     ") {
		t.Errorf("got %q, want no invented alignment padding at unknown width", got)
	}
}

func TestHeaderDefaultsStateRole(t *testing.T) {
	th := loadTheme(t)
	// An unset StateRole must still render the word.
	got := Header(th, theme.TierASCII, 30, HeaderSpec{Marker: " ", Label: "x", State: "pending"})
	if !strings.Contains(got, "pending") {
		t.Errorf("got %q, want the state word", got)
	}
}

// TestFitClipsDetailSuffixBeforeLabel pins C5's own priority order, one
// step below TestHeaderClipsDetailNotState: the DetailSuffix is
// decorative context, so it gives way before Detail, and Detail (like
// the label) still gives way before the label is ever touched.
//
// It asserts on fit() directly, not through Header(): Header's own
// clampWidth backstop truncates the RENDERED row's tail regardless of
// which column gave way internally, and the suffix is always the
// rightmost column - so at some widths a wrong priority order (keep the
// suffix, sacrifice the label) still produces a row that happens not to
// contain the full suffix text, passing a content check for the wrong
// reason. fit() is where the decision is actually made, so it is what
// must be pinned.
func TestFitClipsDetailSuffixBeforeLabel(t *testing.T) {
	// Room for the lead and detail together (15 columns: "v run_command"
	// is 13, plus a space and "x") but not for "waiting for result" (19)
	// beside them. The suffix must give way; the lead and detail must
	// come back untouched - clipDetail was never called on them.
	lead, detail, suffix, _ := fit("v run_command", "x", "waiting for result", "", 20)
	if lead != "v run_command" || detail != "x" {
		t.Errorf("fit(...) = (%q, %q, %q, _), want lead and detail preserved intact", lead, detail, suffix)
	}
	if suffix != "" {
		t.Errorf("fit(...) suffix = %q, want it dropped before the detail gives up any room", suffix)
	}

	// Narrower still: not even the lead fits whole, which is fit()'s own
	// last-resort branch. The suffix must already be empty by the time
	// that branch is reached - it never survives past the point where
	// the label itself has to give way.
	lead, _, suffix, _ = fit("v run_command", "x", "waiting for result", "", 6)
	if suffix != "" {
		t.Errorf("fit(...) suffix = %q, want it clipped before the label", suffix)
	}
	if w := ansi.StringWidth(lead); w > 6 {
		t.Errorf("fit(...) lead = %q, %d columns wider than the 6 available", lead, w)
	}
}

// TestFitClippedDetailReservesItsSeparator pins the fallback budget from
// the caller's side: whatever fit returns must reconstruct, with the
// separators the renderer adds, a row of at most width columns. The
// separator column is owned inside clipDetail (its room parameter
// includes it); this test holds that contract where the row is
// measurable, so an edit that loses the decrement fails here and not
// only as a one-column difference eaten by clampWidth.
func TestFitClippedDetailReservesItsSeparator(t *testing.T) {
	const detail = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" // 40 columns

	// With meta and state on the right (gap of 2): the rendered row is
	// lead + " " + detail + gap + right.
	lead, got, suffix, _ := fit("v run_command", detail, "waiting for result", "running", 40)
	if suffix != "" {
		t.Errorf("suffix = %q, want it dropped before the detail gives up room", suffix)
	}
	rowW := ansi.StringWidth(lead) + 1 + ansi.StringWidth(got) + 2 + ansi.StringWidth("running")
	if rowW > 40 {
		t.Errorf("fit returned lead %q + detail %q: reconstructed row is %d columns at width 40, want at most 40", lead, got, rowW)
	}

	// With nothing on the right there is no gap, but the separator
	// before the detail remains.
	lead, got, _, _ = fit("v edit", detail, "", "", 20)
	rowW = ansi.StringWidth(lead) + 1 + ansi.StringWidth(got)
	if rowW > 20 {
		t.Errorf("fit returned lead %q + detail %q: reconstructed row is %d columns at width 20, want at most 20", lead, got, rowW)
	}
}

// TestHeaderClippedDetailKeepsTheStateWhole pins the visible end of the
// same contract: a clipped detail never costs the state word a column,
// so clampWidth has nothing to eat - at any width, the reader sees the
// whole word.
func TestHeaderClippedDetailKeepsTheStateWhole(t *testing.T) {
	th := loadTheme(t)
	for _, width := range []int{20, 30, 40, 60, 80} {
		got := Header(th, theme.TierASCII, width, HeaderSpec{
			Marker: "v", Label: "run_command",
			Detail:       strings.Repeat("x", 40),
			DetailSuffix: "waiting for result",
			State:        "running",
		})
		if w := ansi.StringWidth(got); w > width {
			t.Errorf("width %d: rendered row is %d columns, want at most %d", width, w, width)
		}
		if p := plain(got); !strings.HasSuffix(p, "running") {
			t.Errorf("width %d: row %q does not end with the whole state word", width, p)
		}
	}
}

func TestClipDetail(t *testing.T) {
	cases := []struct {
		detail string
		room   int
		want   string
		why    string
	}{
		{"", 20, "", "no detail to clip"},
		{"abcdef", 0, "", "no room at all"},
		{"abcdef", 1, "", "room only for the separating space"},
		{"abcdef", 2, "", "room for the space and the marker, nothing else"},
		{"abcdef", 20, "abcdef", "already fits"},
		{"abcdef", 7, "abcdef", "fits exactly"},
		{"abcdef", 5, "abc" + uikitconfig.ClipMarker, "clipped and marked"},
		// Wide runes cost two columns each, so room 7 holds three of
		// them: one space, three runes at two columns, one marker.
		{"漢漢漢漢", 8, "漢漢漢" + uikitconfig.ClipMarker, "clipped by display column, not by rune"},
	}
	for _, c := range cases {
		if got := clipDetail(c.detail, c.room); got != c.want {
			t.Errorf("clipDetail(%q, %d) = %q, want %q (%s)", c.detail, c.room, got, c.want, c.why)
		}
	}
}

// widthHostileSpecs are the inputs that broke the width contract before
// it was enforced: wide runes, combining marks, tabs, and newlines. Each
// can reach a header through a file path or a tool argument.
var widthHostileSpecs = []HeaderSpec{
	{Marker: "v", Label: "edit", Detail: strings.Repeat("漢", 40), Meta: "+4 -1", State: "ok"},
	{Marker: "v", Label: "edit", Detail: strings.Repeat("é", 60), Meta: "+4 -1", State: "ok"},
	{Marker: "v", Label: "run", Detail: "a\tb\tc", Meta: "12ms", State: "ok"},
	{Marker: "v", Label: "run", Detail: "a\nb", State: "failed"},
	{Marker: "v", Label: strings.Repeat("漢", 30), Detail: "x", State: "running"},
	{Marker: "v", Label: "x", State: strings.Repeat("failed ", 20)},
	{Marker: "v", Label: "", Detail: "", Meta: "", State: ""},
	// C5's own column: wide runes and a combining mark beside a normal
	// detail, exercising fitDetailSuffix/clipSuffix rather than clipDetail.
	{Marker: "v", Label: "run_command", Detail: "go vet", DetailSuffix: strings.Repeat("漢", 30), State: "running"},
	{Marker: "v", Label: "run_command", Detail: strings.Repeat("é", 40), DetailSuffix: "waiting for result", State: "running"},
	{Marker: "v", Label: "x", DetailSuffix: "a\tb\nc", State: "pending"},
}

// TestHeaderWidthContract pins the guarantee Block.Height depends on: at
// a known width the header is AT MOST that many columns, on one row,
// with no newline. A header that overflowed would draw two rows while
// the live window budgeted one, and the transcript would outgrow the
// terminal. It is no longer exactly width whenever meta/state are
// present - they sit ragged-right, close after the detail, not padded
// out to the far edge.
func TestHeaderWidthContract(t *testing.T) {
	th := loadTheme(t)
	for _, width := range []int{1, 2, 3, 5, 8, 12, 20, 40, 80, 200} {
		for i, spec := range widthHostileSpecs {
			got := Header(th, theme.TierASCII, width, spec)
			if w := ansi.StringWidth(got); w > width {
				t.Errorf("spec %d at width %d: got width %d, want at most %d:\n%q",
					i, width, w, width, got)
			}
			if strings.ContainsAny(plain(got), "\n\r\t") {
				t.Errorf("spec %d at width %d: control character in a header row: %q",
					i, width, got)
			}
		}
	}
}

// TestHeaderKeepsTheStateWhenNothingElseFits pins the priority order at
// a hostile width: the state word is the last thing to go.
func TestHeaderKeepsTheStateWhenNothingElseFits(t *testing.T) {
	th := loadTheme(t)
	got := plain(Header(th, theme.TierASCII, 6, HeaderSpec{
		Marker: "v", Label: "run_command", Detail: "x", State: "failed",
	}))
	if got != "failed" {
		t.Errorf("got %q, want the state alone at a width that fits nothing else", got)
	}
}

// FuzzHeader hunts the width contract on input no table would think to
// write. Both assertions are the contract itself, not a golden.
func FuzzHeader(f *testing.F) {
	for _, s := range widthHostileSpecs {
		f.Add(s.Marker, s.Label, s.Detail, s.Meta, s.State, s.DetailSuffix, 40)
	}
	f.Add("v", "edit", "main.go", "+1", "ok", "", 80)
	// C5's column, so random inputs exercise fitDetailSuffix/clipSuffix;
	// before this seed the fuzzer never set a suffix at all.
	f.Add("v", "run_command", "go vet ./...", "4.1s", "running", "waiting for result", 40)
	th := loadTheme(f)
	f.Fuzz(func(t *testing.T, marker, label, detail, meta, state, suffix string, width int) {
		// Bound the width: the contract is about layout, and a huge
		// allocation proves nothing.
		if width < -4 || width > 400 {
			t.Skip()
		}
		got := Header(th, theme.TierASCII, width, HeaderSpec{
			Marker: marker, Label: label, Detail: detail, Meta: meta, State: state,
			DetailSuffix: suffix,
		})
		if strings.ContainsAny(plain(got), "\n\r\t") {
			t.Fatalf("control character in a header row: %q", got)
		}
		if width <= 0 {
			return
		}
		// The fuzz guards the SAFETY property only: never wider than the
		// terminal, never a second row. Exact right-alignment is a layout
		// property, pinned by the tables above on input whose display
		// width is well defined.
		if w := ansi.StringWidth(got); w > width {
			t.Fatalf("got width %d, want at most %d: %q", w, width, got)
		}
	})
}

// TestClampWidth covers the backstop directly. It is the only guarantee
// that survives every grapheme-cluster surprise, so it is tested on its
// own rather than only through Header.
func TestClampWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
	}{
		{"already fits", "abc", 10},
		{"plain cut", "abcdefgh", 3},
		{"wide runes", strings.Repeat("漢", 10), 5},
		// A variation selector widens the rune it follows, and it can end
		// up on the far side of a cut. One truncation is then not enough.
		{"variation selector", "0️0️0", 2},
		{"nothing fits", strings.Repeat("漢", 4), 1},
	}
	for _, c := range cases {
		got := clampWidth(c.in, c.width)
		if w := ansi.StringWidth(got); w > c.width {
			t.Errorf("%s: clampWidth(%q,%d) = %q at %d columns, want at most %d",
				c.name, c.in, c.width, got, w, c.width)
		}
	}
}

// TestClampWidthGivesUpRatherThanOverflow pins the loop's exit: when no
// prefix fits, the row is empty. An overflowing row is never acceptable.
func TestClampWidthGivesUpRatherThanOverflow(t *testing.T) {
	if got := clampWidth("漢", 1); ansi.StringWidth(got) > 1 {
		t.Errorf("got %q, want a row no wider than 1 column", got)
	}
}

func TestHeaderDiffColoring(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierTrueColor, 80, HeaderSpec{
		Marker:  "v",
		Label:   "search_replace",
		Detail:  "internal/ui/render/header.go",
		DiffAdd: 12,
		DiffDel: 3,
		Meta:    "14ms",
		State:   "ok",
	})
	if !strings.Contains(got, "+12") || !strings.Contains(got, "-3") {
		t.Fatalf("expected +12 and -3 in rendered header, got:\n%s", got)
	}
	if !strings.Contains(got, "14ms") || !strings.Contains(got, "ok") {
		t.Fatalf("expected 14ms and ok in rendered header, got:\n%s", got)
	}
	// Verify color escape exists around +12
	addRole := Role(th, theme.TierTrueColor, theme.RoleDiffAddFG).Render("+12")
	if !strings.Contains(got, addRole) {
		t.Errorf("expected colored diff addition %q in output:\n%s", addRole, got)
	}
	delRole := Role(th, theme.TierTrueColor, theme.RoleDiffDelFG).Render("-3")
	if !strings.Contains(got, delRole) {
		t.Errorf("expected colored diff deletion %q in output:\n%s", delRole, got)
	}
}

func TestHeaderDiffTierDegradation(t *testing.T) {
	th := loadTheme(t)
	got := Header(th, theme.TierASCII, 80, HeaderSpec{
		Marker:  "v",
		Label:   "search_replace",
		Detail:  "main.go",
		DiffAdd: 5,
		DiffDel: 2,
		Meta:    "8ms",
		State:   "ok",
	})
	p := plain(got)
	if !strings.Contains(p, "+5 -2") {
		t.Errorf("expected '+5 -2' in ASCII degraded header, got %q", p)
	}
}

func TestSectionHeader(t *testing.T) {
	th := loadTheme(t)
	hdr := SectionHeader(th, theme.TierTrueColor, "Behavior", 40)
	p := plain(hdr)
	if !strings.HasPrefix(p, "◆ Behavior ") {
		t.Errorf("expected header to start with '◆ Behavior ', got %q", p)
	}
	if !strings.Contains(p, "─") {
		t.Errorf("expected divider rune '─' in header, got %q", p)
	}
	if ansi.StringWidth(p) > 40 {
		t.Errorf("width %d exceeds 40", ansi.StringWidth(p))
	}

	// ASCII degradation
	hdrAscii := SectionHeader(th, theme.TierASCII, "Behavior", 30)
	pAscii := plain(hdrAscii)
	if !strings.HasPrefix(pAscii, "> Behavior ") {
		t.Errorf("expected ASCII header to start with '> Behavior ', got %q", pAscii)
	}
	if !strings.Contains(pAscii, "-") {
		t.Errorf("expected divider '-' in ASCII header, got %q", pAscii)
	}
}
