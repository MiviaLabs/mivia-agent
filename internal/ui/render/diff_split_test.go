package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func sampleSplitDiff() uievent.Diff {
	return uievent.Diff{
		Path:    "pkg/example.go",
		Added:   2,
		Removed: 1,
		Hunks: []uievent.DiffHunk{
			{
				Header: "@@ -10,3 +10,4 @@ func Run() {",
				Lines: []uievent.DiffLine{
					{Kind: uievent.DiffLineContext, Text: "func Run() {"},
					{Kind: uievent.DiffLineDel, Text: "\toldLogic()"},
					{Kind: uievent.DiffLineAdd, Text: "\tnewLogicA()"},
					{Kind: uievent.DiffLineAdd, Text: "\tnewLogicB()"},
					{Kind: uievent.DiffLineContext, Text: "}"},
				},
			},
		},
	}
}

func TestSplitDiffLinesRendersBothColumns(t *testing.T) {
	th := loadTheme(t)
	d := sampleSplitDiff()
	lines := SplitDiffLines(th, theme.TierTrueColor, 80, d)
	if len(lines) == 0 {
		t.Fatal("SplitDiffLines returned no lines")
	}

	// First line is hunk header
	if !strings.Contains(lines[0], "@@") {
		t.Errorf("expected hunk header on line 0, got %q", lines[0])
	}

	// Content lines contain divider
	for i := 1; i < len(lines); i++ {
		plain := ansi.Strip(lines[i])
		if !strings.Contains(plain, "│") {
			t.Errorf("line %d missing column divider: %q", i, plain)
		}
	}

	// Test change pairing: deletion on left, addition on right
	foundDel, foundAdd := false, false
	for _, l := range lines {
		plain := ansi.Strip(l)
		if strings.Contains(plain, "oldLogic()") {
			foundDel = true
		}
		if strings.Contains(plain, "newLogicA()") {
			foundAdd = true
		}
	}
	if !foundDel || !foundAdd {
		t.Errorf("expected deletion and addition in split output; got del=%v, add=%v", foundDel, foundAdd)
	}
}

// TestFormatDiffLinesUnifiedByDefault pins C8: unified is the default at
// EVERY width, including widths that used to auto-split under the old
// 60-column threshold. Split only happens when the caller passes
// split=true.
func TestFormatDiffLinesUnifiedByDefault(t *testing.T) {
	th := loadTheme(t)
	d := sampleSplitDiff()

	for _, width := range []int{40, 80, 120, 160, 200} {
		lines := FormatDiffLines(th, theme.TierTrueColor, width, d, false)
		if len(lines) == 0 {
			t.Fatalf("width %d: FormatDiffLines returned no lines", width)
		}
		for i := 1; i < len(lines); i++ {
			if strings.Contains(ansi.Strip(lines[i]), "│") {
				t.Errorf("width %d: split=false must never render a column divider, got %q", width, lines[i])
			}
		}
	}
}

// TestFormatDiffLinesResponsiveFallback pins the two-part contract:
// split=true is honored above MinSplitDiffWidth, and refused (falls back
// to unified) below it even when requested - a resize after a block
// already asked for split must not render illegibly.
func TestFormatDiffLinesResponsiveFallback(t *testing.T) {
	th := loadTheme(t)
	d := sampleSplitDiff()

	// Below MinSplitDiffWidth, split=true is still refused.
	narrow := FormatDiffLines(th, theme.TierTrueColor, 40, d, true)
	if len(narrow) == 0 {
		t.Fatal("FormatDiffLines returned no lines for narrow width")
	}
	for i := 1; i < len(narrow); i++ {
		if strings.Contains(ansi.Strip(narrow[i]), "│") {
			t.Errorf("narrow diff should not contain column divider: %q", narrow[i])
		}
	}

	// At or above MinSplitDiffWidth, split=true uses split columns.
	wide := FormatDiffLines(th, theme.TierTrueColor, MinSplitDiffWidth, d, true)
	foundDivider := false
	for i := 1; i < len(wide); i++ {
		if strings.Contains(ansi.Strip(wide[i]), "│") {
			foundDivider = true
			break
		}
	}
	if !foundDivider {
		t.Errorf("wide diff expected column divider, got:\n%s", strings.Join(wide, "\n"))
	}
}

// TestSplitDiffAt160UsesMoreThan92Columns pins C8's width-exemption
// claim: a split diff is not capped at the prose measure
// (uikitconfig.ProseMeasureWide == 92). At a generous width it actually
// uses the room it is given.
func TestSplitDiffAt160UsesMoreThan92Columns(t *testing.T) {
	th := loadTheme(t)
	d := sampleSplitDiff()

	lines := FormatDiffLines(th, theme.TierTrueColor, 160, d, true)
	widest := 0
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > widest {
			widest = w
		}
	}
	if widest <= 92 {
		t.Errorf("split diff at width 160 is %d columns wide, want more than 92 (ProseMeasureWide) - it must not be measured as prose", widest)
	}
}

func TestParseHunkHeader(t *testing.T) {
	oldS, newS := parseHunkHeader("@@ -42,5 +108,12 @@ type Foo struct")
	if oldS != 42 || newS != 108 {
		t.Errorf("parseHunkHeader got (%d, %d), want (42, 108)", oldS, newS)
	}

	oldS2, newS2 := parseHunkHeader("@@ -1 +1 @@")
	if oldS2 != 1 || newS2 != 1 {
		t.Errorf("parseHunkHeader single got (%d, %d), want (1, 1)", oldS2, newS2)
	}
}
