package diff

import (
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
