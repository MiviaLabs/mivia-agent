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
	// No detail: clipLeft has nothing to give up and must report so
	// rather than trimming the label.
	got := Header(th, theme.TierASCII, 10, HeaderSpec{
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

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"hello", 5, "hello"},
		{"hello", 10, "hello"},
		{"hello", 2, "he"},
		{"héllo", 2, "hé"}, // multi-byte: cut by rune, never mid-character
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestClipLeftGuards covers the two refusals: no room left for even a
// clipped detail, and a detail already short enough to need no clip.
func TestClipLeftGuards(t *testing.T) {
	spec := HeaderSpec{Marker: "v", Label: "run_command", Detail: "abcdef"}
	left := "v run_command abcdef"

	// Budget too small for the prefix plus a clip marker.
	if _, ok := clipLeft(spec, left, 5); ok {
		t.Error("expected no clip when there is no room for one")
	}
	// Detail already fits the budget.
	if _, ok := clipLeft(spec, left, 100); ok {
		t.Error("expected no clip when the detail already fits")
	}
	// Genuinely clippable.
	got, ok := clipLeft(spec, left, 18) // prefix 14 + marker 1 leaves room 3 < len("abcdef")
	if !ok {
		t.Fatalf("expected a clip at a workable budget, got %q", got)
	}
	if !strings.HasSuffix(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want it to end with the clip marker", got)
	}
}
