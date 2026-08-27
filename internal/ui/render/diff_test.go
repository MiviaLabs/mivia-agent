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

// TestDiffLinesWindowsByIndex pins the sliceable form: one string per
// rendered line, hunk headers included, so a windowing surface can index
// it without re-parsing styled text.
func TestDiffLinesWindowsByIndex(t *testing.T) {
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
	lines := DiffLines(loadTheme(t), theme.TierASCII, d)
	if len(lines) != DiffLineCount(d) || len(lines) != 6 {
		t.Fatalf("DiffLines = %d lines, DiffLineCount = %d, want 6", len(lines), DiffLineCount(d))
	}
	if got := ansi.Strip(strings.Join(lines[4:6], "\n")); got != "@@ -9,2 +9,2 @@\n+ later" {
		t.Errorf("window [4:6] = %q", got)
	}
	if DiffLines(loadTheme(t), theme.TierASCII, uievent.Diff{Path: "a.go"}) != nil {
		t.Error("empty diff rendered lines, want nil")
	}
}
