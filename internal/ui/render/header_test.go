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

func TestHeaderRightAlignsMetaAndState(t *testing.T) {
	th := loadTheme(t)
	const width = 60
	got := Header(th, theme.TierASCII, width, HeaderSpec{
		Marker: "v", Label: "edit", Detail: "main.go", Meta: "+4 -1", State: "ok",
	})
	if w := lipgloss.Width(got); w != width {
		t.Errorf("got width %d, want exactly %d", w, width)
	}
	if !strings.HasSuffix(plain(got), "ok") {
		t.Errorf("got %q, want the state flush to the right edge", got)
	}
	if !strings.HasPrefix(plain(got), "v edit") {
		t.Errorf("got %q, want the marker and label at the left", got)
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
	if w := lipgloss.Width(got); w != width {
		t.Errorf("got width %d, want %d", w, width)
	}
	if !strings.HasSuffix(plain(got), "running") {
		t.Errorf("got %q, want the state right-aligned", got)
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
	if w := lipgloss.Width(got); w != width {
		t.Errorf("got width %d, want exactly %d:\n%q", w, width, got)
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
}

// TestHeaderWidthContract pins the guarantee Block.Height depends on: at
// a known width the header is EXACTLY that many columns and holds no
// newline. A header that overflowed would draw two rows while the live
// window budgeted one, and the transcript would outgrow the terminal.
func TestHeaderWidthContract(t *testing.T) {
	th := loadTheme(t)
	for _, width := range []int{1, 2, 3, 5, 8, 12, 20, 40, 80, 200} {
		for i, spec := range widthHostileSpecs {
			got := Header(th, theme.TierASCII, width, spec)
			if w := ansi.StringWidth(got); w > width {
				t.Errorf("spec %d at width %d: got width %d, want at most %d:\n%q",
					i, width, w, width, got)
			}
			// A right column forces the row to fill: that is what makes
			// the meta and state right-aligned rather than merely last.
			if w := ansi.StringWidth(got); (spec.Meta != "" || spec.State != "") && w != width {
				t.Errorf("spec %d at width %d: got width %d, want exactly %d:\n%q",
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
		f.Add(s.Marker, s.Label, s.Detail, s.Meta, s.State, 40)
	}
	f.Add("v", "edit", "main.go", "+1", "ok", 80)
	th := loadTheme(f)
	f.Fuzz(func(t *testing.T, marker, label, detail, meta, state string, width int) {
		// Bound the width: the contract is about layout, and a huge
		// allocation proves nothing.
		if width < -4 || width > 400 {
			t.Skip()
		}
		got := Header(th, theme.TierASCII, width, HeaderSpec{
			Marker: marker, Label: label, Detail: detail, Meta: meta, State: state,
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
