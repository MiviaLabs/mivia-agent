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

func TestFormatDiffLinesResponsiveFallback(t *testing.T) {
	th := loadTheme(t)
	d := sampleSplitDiff()

	// Below MinSplitDiffWidth falls back to unified diff
	narrow := FormatDiffLines(th, theme.TierTrueColor, 40, d)
	if len(narrow) == 0 {
		t.Fatal("FormatDiffLines returned no lines for narrow width")
	}
	for i := 1; i < len(narrow); i++ {
		if strings.Contains(ansi.Strip(narrow[i]), "│") {
			t.Errorf("narrow diff should not contain column divider: %q", narrow[i])
		}
	}

	// At wide width uses split columns
	wide := FormatDiffLines(th, theme.TierTrueColor, 80, d)
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
