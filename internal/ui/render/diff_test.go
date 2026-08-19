package render

import (
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
	for _, want := range []string{"main.go", "+1 -1", "@@ -1,2 +1,2 @@", "  package main", "- old", "+ new"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff output missing %q:\n%s", want, got)
		}
	}
}
