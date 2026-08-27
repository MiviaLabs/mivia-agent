package diff

import (
	"fmt"
	"strings"
	"testing"
)

// TestDiffMultiHunkStartsAnchoredToTrueLines reproduces the hunk-start
// double-count bug: a 22-line pair with single-line changes at old lines 3,
// 11 and 19, context 3, anchored at the first changed line (3). The third
// hunk's body starts at old line 16 (its leading context is l16, l17, l18),
// so its header must read "@@ -16,7 +16,7 @@". Before the fix, contextHunks
// kept a running oldBefore/newBefore total that was never reset per interval,
// so the third interval re-counted the prefixes of both earlier intervals and
// rendered "@@ -23,7 +23,7 @@" past EOF instead.
func TestDiffMultiHunkStartsAnchoredToTrueLines(t *testing.T) {
	oldLines := make([]string, 22)
	newLines := make([]string, 22)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("l%d", i+1)
	}
	copy(newLines, oldLines)
	// Single-line replacements at old lines 3, 11 and 19 (0-based 2, 10, 18).
	for _, idx := range []int{2, 10, 18} {
		oldLines[idx] = fmt.Sprintf("o%d", idx+1)
		newLines[idx] = fmt.Sprintf("n%d", idx+1)
	}
	oldText, newText := strings.Join(oldLines, "\n"), strings.Join(newLines, "\n")
	r, err := Compute(oldText, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 3, 3, 3)
	want := []string{"@@ -1,6 +1,6 @@", "@@ -8,7 +8,7 @@", "@@ -16,7 +16,7 @@"}
	for _, h := range want {
		if !strings.Contains(got, h) {
			t.Fatalf("missing hunk header %q in %q", h, got)
		}
	}
	if strings.Contains(got, "@@ -23,") {
		t.Fatalf("hunk start double-counted earlier intervals, got %q", got)
	}
}
