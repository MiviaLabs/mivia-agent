package diff

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiff_A2_CascadeInsert(t *testing.T) {
	r, err := Compute("a\nb\nc", "a\nx\nb\nc", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del := Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("stats=%d/%d", ins, del)
	}
	got := FormatUnified("x.txt", r)
	want := "--- a/x.txt\n+++ b/x.txt\n@@ -1,3 +1,4 @@\n a\n+x\n b\n c"
	if got != want {
		t.Fatalf("diff=%q", got)
	}
}

func TestDiff_A1_OraclePairs(t *testing.T) {
	cases := [][2]string{{"", ""}, {"a", "b"}, {"a", "a\nb"}, {"a\nb", "a"}, {"a\nb", "b\na"}, {"a\nb\nc", "a\nc"}, {"a\nc", "a\nb\nc"}, {"one\ntwo", "one\nTWO"}, {"x\ny\nz", "x\ny\nz"}, {"x\n", "x"}, {"x", "x\n"}, {"a\nb\nc", "q\na\nb\nc"}, {"a\nb\nc", "a\nb\nc\nq"}, {"a\nb\nc", "a\nq\nc"}, {"a\nb\nc", "q\nb\nr"}, {"same", "same"}}
	for i, c := range cases {
		if _, err := Compute(c[0], c[1], Options{}); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

func TestDiff_BoundedAndUTF8Safe(t *testing.T) {
	if _, err := Compute("12345", "x", Options{MaxInputBytes: 4}); err == nil {
		t.Fatal("expected input cap")
	}
	if got := TruncateUTF8("界界", 4); got != "界" {
		t.Fatalf("got %q", got)
	}
}

func TestDiff_ContextIsPerLineAndHeaderIsFileRelative(t *testing.T) {
	old := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10"
	newText := "l1\nl2\nl3\nl4\nchanged\nl6\nl7\nl8\nl9\nl10"
	r, err := Compute(old, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 5, 5, 3)
	if !strings.Contains(got, "@@ -2,7 +2,7 @@") {
		t.Fatalf("header=%q", got)
	}
	if strings.Contains(got, " l1\n") || strings.Contains(got, " l9\n") {
		t.Fatalf("context exceeded radius: %q", got)
	}
}

func TestDiff_TrailingNewlineChangeCounts(t *testing.T) {
	r, err := Compute("a", "a\n", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del := Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("stats=%d/%d, want 1/0", ins, del)
	}
}

func TestDiff_SeparateHunksAndNewlineMetadata(t *testing.T) {
	r, err := Compute("a\n1\n2\n3\n4\nb", "x\n1\n2\n3\n4\ny", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 1, 1, 1)
	if strings.Count(got, "@@ ") != 2 {
		t.Fatalf("hunks=%q", got)
	}
	r, err = Compute("a\n", "a\nb", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del := Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("stats=%d/%d", ins, del)
	}
}

func TestDiff_ZeroContextIsFullDiff(t *testing.T) {
	// context=0 previously produced degenerate empty-content hunks per change.
	// After fix it should produce a single full-diff hunk (same as context < 0).
	old := "a\nb\nc"
	newText := "x\nb\nc"
	r, err := Compute(old, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	gotZero := FormatUnifiedAt("x", r, 1, 1, 0)
	gotNeg := FormatUnifiedAt("x", r, 1, 1, -1)
	if gotZero != gotNeg {
		t.Fatalf("context=0 should equal context=-1 (full diff)\n  0: %q\n -1: %q", gotZero, gotNeg)
	}
	// Verify it contains the actual change, not an empty hunk.
	if !strings.Contains(gotZero, "-a\n") || !strings.Contains(gotZero, "+x\n") {
		t.Fatalf("context=0 diff missing content: %q", gotZero)
	}
	// Verify only one hunk header.
	if strings.Count(gotZero, "@@ ") != 1 {
		t.Fatalf("context=0 should produce exactly one hunk: %q", gotZero)
	}
}

func TestDiff_ZeroContextMultipleChanges(t *testing.T) {
	// Two separate changes with context=0 should still produce a single full-diff hunk.
	old := "a\n1\n2\n3\n4\nb"
	newText := "x\n1\n2\n3\n4\ny"
	r, err := Compute(old, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	gotZero := FormatUnifiedAt("x", r, 1, 1, 0)
	// Should NOT split into two hunks — that was the old broken behavior.
	if strings.Count(gotZero, "@@ ") != 1 {
		t.Fatalf("context=0 should produce exactly one hunk for multiple changes: %q", gotZero)
	}
	if !strings.Contains(gotZero, "-a\n") || !strings.Contains(gotZero, "+x\n") {
		t.Fatalf("context=0 diff missing first change: %q", gotZero)
	}
	if !strings.Contains(gotZero, "-b\n") || !strings.Contains(gotZero, "+y\n") {
		t.Fatalf("context=0 diff missing second change: %q", gotZero)
	}
}

func TestDiff_ZeroContextAllEqual(t *testing.T) {
	// No changes with context=0 should still produce valid output.
	r, err := Compute("a\nb\nc", "a\nb\nc", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnifiedAt("x", r, 1, 1, 0)
	if !strings.Contains(got, "@@ -1,3 +1,3 @@") {
		t.Fatalf("context=0 with no changes should show full file: %q", got)
	}
}

func TestDiff_EmptyInputPairs(t *testing.T) {
	// Empty-to-empty should produce a valid (but trivial) unified diff.
	r, err := Compute("", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := FormatUnified("x", r)
	if !strings.HasPrefix(got, "--- a/x\n+++ b/x") {
		t.Fatalf("empty-to-empty diff: %q", got)
	}
	// Empty-to-something.
	r, err = Compute("", "hello", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del := Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("empty-to-hello stats=%d/%d", ins, del)
	}
	// Something-to-empty.
	r, err = Compute("hello", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del = Stats(r)
	if ins != 0 || del != 1 {
		t.Fatalf("hello-to-empty stats=%d/%d", ins, del)
	}
}

// TestDiff_TrailingNewlineRemovalIsVisible pins that removing a file's only
// trailing newline is a real change. Before the fix, Compute stripped the
// old-side empty terminal whenever the new side lacked one, so "a\nx\n" ->
// "a\nx" produced an all-Equal diff with Stats 0/0 (dishonest +0 -0) instead
// of a visible Delete with del=1.
func TestDiff_TrailingNewlineRemovalIsVisible(t *testing.T) {
	r, err := Compute("a\nx\n", "a\nx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del := Stats(r)
	if ins != 0 || del != 1 {
		t.Fatalf("removal stats=%d/%d, want 0/1", ins, del)
	}
	// Negative path: the add direction stays ins=1, del=0.
	r, err = Compute("a\nx", "a\nx\n", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del = Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("add stats=%d/%d, want 1/0", ins, del)
	}
	// Pinned: appending a line after a trailing newline stays ins=1, del=0.
	r, err = Compute("a\n", "a\nb", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ins, del = Stats(r)
	if ins != 1 || del != 0 {
		t.Fatalf("append stats=%d/%d, want 1/0", ins, del)
	}
}

// TestDiff_TrailingNewlineRemovalHunkWithinBounds reproduces the finding's
// exact scenario: "a\nx\n" edited to "a\nx" removes a trailing newline, and
// callers anchor hunks at firstChangedLine computed from raw strings.Split
// (3 for this pair). The emitted hunk must stay within the file's raw line
// counts instead of "@@ -3,2 +3,2 @@" beyond EOF with no change lines.
func TestDiff_TrailingNewlineRemovalHunkWithinBounds(t *testing.T) {
	oldText, newText := "a\nx\n", "a\nx"
	r, err := Compute(oldText, newText, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// anchor 3 is the edit.go firstChangedLine path; anchor 1 is the
	// write_file path. Both must name an in-bounds header and show the
	// deletion of the empty terminal line.
	for _, anchor := range []int{3, 1} {
		got := FormatUnifiedAt("x", r, anchor, anchor, 3)
		if !strings.Contains(got, "@@ -1,3 +1,2 @@") {
			t.Fatalf("anchor %d header missing, got %q", anchor, got)
		}
		if !strings.HasSuffix(got, "-\n") {
			t.Fatalf("anchor %d diff missing visible deletion of the empty terminal, got %q", anchor, got)
		}
		assertHunkHeadersWithinBounds(t, got, oldText, newText)
	}
}

// assertHunkHeadersWithinBounds parses each hunk header in a unified diff and
// fails unless the named old/new line ranges lie within the raw-split line
// counts of the two sides.
func assertHunkHeadersWithinBounds(t *testing.T, got, oldText, newText string) {
	t.Helper()
	oldCount := len(strings.Split(oldText, "\n"))
	newCount := len(strings.Split(newText, "\n"))
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		var oldStart, oldLines, newStart, newLines int
		if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldStart, &oldLines, &newStart, &newLines); err != nil {
			t.Fatalf("unparseable hunk header %q: %v", line, err)
		}
		if oldStart < 1 || oldStart+oldLines-1 > oldCount {
			t.Fatalf("old range %d..%d exceeds raw old line count %d in %q", oldStart, oldStart+oldLines-1, oldCount, line)
		}
		if newStart < 1 || newStart+newLines-1 > newCount {
			t.Fatalf("new range %d..%d exceeds raw new line count %d in %q", newStart, newStart+newLines-1, newCount, line)
		}
	}
}
