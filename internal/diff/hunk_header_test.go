package diff

import (
	"strings"
	"testing"
)

// TestDiffSecondHunkHeaderStart pins the hunk-start arithmetic for the second
// hunk of a multi-hunk diff. The second hunk's first op is E "4", which covers
// old line 5 and new line 5, so its header must read "@@ -5,2 +5,2 @@". Before
// the fix, contextHunks subtracted each hunk's own leading context again and
// emitted "@@ -4,2 +4,2 @@" instead.
func TestDiffSecondHunkHeaderStart(t *testing.T) {
	r, err := Compute("a\n1\n2\n3\n4\nb", "x\n1\n2\n3\n4\ny", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 1, 1, 1)
	if !strings.Contains(got, "@@ -5,2 +5,2 @@") {
		t.Fatalf("second hunk header missing, got %q, want @@ -5,2 +5,2 @@", got)
	}
	if !strings.Contains(got, "@@ -1,2 +1,2 @@") {
		t.Fatalf("first hunk header missing, got %q", got)
	}
	if strings.Contains(got, "@@ -4,2 +4,2 @@") {
		t.Fatalf("second hunk header over-shifted, got %q", got)
	}
}

// TestDiffHunkHeaderStartNeverBelowOne pins the clamp that keeps every hunk
// start at or above 1. FormatUnifiedAt clamps its inputs, so a hunk start of 0
// or a negative start is invalid unified diff output. Before the fix, a diff
// whose leading context reaches the top of the file emitted "@@ -0,5 +0,5 @@"
// (and deeper leading context went negative, "@@ --N,... @@").
func TestDiffHunkHeaderStartNeverBelowOne(t *testing.T) {
	r, err := Compute("a\nb\nc\nd\ne", "a\nX\nc\nd\ne", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 1, 1, 3)
	if strings.Contains(got, "@@ -0,") {
		t.Fatalf("hunk start below one, got %q", got)
	}
	if strings.Contains(got, "@@ --") {
		t.Fatalf("negative hunk start, got %q", got)
	}
	if !strings.Contains(got, "@@ -1,5 +1,5 @@") {
		t.Fatalf("header missing, got %q, want @@ -1,5 +1,5 @@", got)
	}
}
