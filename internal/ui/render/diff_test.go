package render

import (
	"github.com/charmbracelet/x/ansi"

	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestDiffEmpty(t *testing.T) {
	th := loadTheme(t)
	if got := Diff(th, theme.TierTrueColor, uievent.Diff{}); got != "" {
		t.Errorf("got %q, want empty string for a diff with no hunks", got)
	}
}

func TestDiffContainsPathAndLines(t *testing.T) {
	th := loadTheme(t)
	d := uievent.Diff{
		Path: "main.go", Added: 1, Removed: 1,
		Hunks: []uievent.DiffHunk{{
			Header: "@@ -1,2 +1,2 @@",
			Lines: []uievent.DiffLine{
				{Kind: uievent.DiffLineContext, Text: "package main"},
				{Kind: uievent.DiffLineDel, Text: "old"},
				{Kind: uievent.DiffLineAdd, Text: "new"},
			},
		}},
	}
	got := Diff(th, theme.TierASCII, d) // ASCII tier: no colour, so substrings survive verbatim
	// No summary line: the path and counts live in the block header.
	for _, notWant := range []string{"main.go", "+1 -1"} {
		if strings.Contains(got, notWant) {
			t.Errorf("diff must not render its own summary line, found %q:\n%s", notWant, got)
		}
	}
	for _, want := range []string{"@@ -1,2 +1,2 @@", "  package main", "- old", "+ new"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff output missing %q:\n%s", want, got)
		}
	}
}

func TestDiffSeparatesMultipleHunks(t *testing.T) {
	th := loadTheme(t)
	d := uievent.Diff{
		Path: "main.go",
		Hunks: []uievent.DiffHunk{
			{Header: "@@ -1,1 +1,1 @@", Lines: []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "a"}}},
			{Header: "@@ -9,1 +9,1 @@", Lines: []uievent.DiffLine{{Kind: uievent.DiffLineDel, Text: "b"}}},
		},
	}
	got := Diff(th, theme.TierASCII, d)
	rows := strings.Split(got, "\n")
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 (two headers, two lines):\n%s", len(rows), got)
	}
	if !strings.Contains(rows[0], "@@ -1,1") || !strings.Contains(rows[2], "@@ -9,1") {
		t.Errorf("hunks not separated correctly:\n%s", got)
	}
}

// TestDiffPreviewCapsAndCounts pins the preview: it shows the first
// maxLines rendered lines (hunk headers count), appends a "N more lines"
// note only when the cap cut something, and renders nothing for a diff
// with no hunks.
func TestDiffPreviewCapsAndCounts(t *testing.T) {
	d := uievent.Diff{
		Path: "a.go",
		Hunks: []uievent.DiffHunk{
			{Header: "@@ -1,3 +1,3 @@", Lines: []uievent.DiffLine{
				{Kind: uievent.DiffLineDel, Text: "old"},
				{Kind: uievent.DiffLineAdd, Text: "new"},
				{Kind: uievent.DiffLineContext, Text: "same"},
			}},
			{Header: "@@ -9,2 +9,2 @@", Lines: []uievent.DiffLine{
				{Kind: uievent.DiffLineAdd, Text: "later"},
			}},
		},
	}
	got := DiffPreview(loadTheme(t), theme.TierASCII, d, 4)
	plain := strings.ReplaceAll(ansi.Strip(got), "\n", "|")
	want := "@@ -1,3 +1,3 @@|- old|+ new|  same|2 more lines"
	if plain != want {
		t.Errorf("DiffPreview = %q, want %q", plain, want)
	}
	if got := DiffPreview(loadTheme(t), theme.TierASCII, d, 10); strings.Contains(got, "more lines") {
		t.Errorf("uncapped preview carries a more-lines note:\n%s", got)
	}
	if got := DiffPreview(loadTheme(t), theme.TierASCII, uievent.Diff{Path: "a.go"}, 4); got != "" {
		t.Errorf("empty diff rendered %q, want empty", got)
	}
}
