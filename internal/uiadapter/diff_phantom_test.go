package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// parseDiffHunks must not turn separator artifacts into visible diff rows.
// A blank line just before a hunk header is a separator, and the empty
// final split element of an output that ends with a newline is the
// terminator; both rendered as empty rows in the TUI.
func TestParseDiffHunks_DropsSeparatorAndTerminatorEmptyLines(t *testing.T) {
	// Shape matches internal/diff.FormatUnifiedAt before its separator fix:
	// a blank line between hunks and a trailing newline after the last
	// change line.
	output := "--- a/f.go\n" +
		"+++ b/f.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" a\n" +
		"-old\n" +
		"+new\n" +
		" 1\n" +
		"\n" +
		"@@ -14,3 +14,3 @@\n" +
		" 13\n" +
		" 14\n" +
		"-b\n" +
		"+y\n"

	hunks, added, removed, path := parseDiffHunks(output)
	if path != "f.go" {
		t.Fatalf("path=%q, want f.go", path)
	}
	if added != 2 || removed != 2 {
		t.Fatalf("added=%d removed=%d, want 2/2", added, removed)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks=%d, want 2", len(hunks))
	}
	wantLens := []int{4, 4} // a/-old/+new/1 and 13/14/-b/+y
	for i, h := range hunks {
		if len(h.Lines) != wantLens[i] {
			t.Fatalf("hunk %d has %d lines, want %d: %+v", i, len(h.Lines), wantLens[i], h.Lines)
		}
		for _, l := range h.Lines {
			if l.Kind == uievent.DiffLineContext && l.Text == "" {
				t.Fatalf("hunk %d carries an empty context row (renders as an empty row): %+v", i, h.Lines)
			}
		}
	}
}

// A blank line in the middle of a hunk is real content: tools that trim
// trailing whitespace emit a blank context row without its leading space.
// The separator drop must keep those.
func TestParseDiffHunks_KeepsBlankContextInsideHunk(t *testing.T) {
	output := "--- a/g.txt\n" +
		"+++ b/g.txt\n" +
		"@@ -1,3 +1,3 @@\n" +
		" first\n" +
		"\n" +
		" last\n"

	hunks, _, _, _ := parseDiffHunks(output)
	if len(hunks) != 1 {
		t.Fatalf("hunks=%d, want 1", len(hunks))
	}
	lines := hunks[0].Lines
	if len(lines) != 3 {
		t.Fatalf("lines=%d, want 3 (the final newline element must not count): %+v", len(lines), lines)
	}
	if lines[1].Kind != uievent.DiffLineContext || lines[1].Text != "" {
		t.Fatalf("mid-hunk blank context lost: %+v", lines)
	}
	if lines[2].Text != "last" {
		t.Fatalf("last line = %q, want %q", lines[2].Text, "last")
	}
}
