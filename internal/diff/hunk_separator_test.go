package diff

import (
	"strings"
	"testing"
)

// FormatUnifiedAt joins hunks back to back. writeHunk ends every line with
// a newline, so one extra byte between hunks turns into a blank line. The
// TUI parses this text back into rows, so a blank line shows there as an
// empty row between hunks. Standard unified diffs (git, diff -u) put hunks
// next to each other with no separator line.
func TestFormatUnifiedAt_NoBlankLineBetweenHunks(t *testing.T) {
	oldText := "a\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\nb"
	newText := "x\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\ny"
	r, err := Compute(oldText, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("f.go", r, 1, 1, 3)
	if strings.Count(got, "@@ ") != 2 {
		t.Fatalf("fixture must produce 2 hunks, got:\n%s", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("blank line between hunks renders as an empty row in the TUI:\n%q", got)
	}
	// The final newline after a +/- line stays (existing convention); only
	// interior blank lines are forbidden.
	if !strings.HasSuffix(got, "+y\n") {
		t.Fatalf("diff must end with the change line and its newline, got %q", got)
	}
}
