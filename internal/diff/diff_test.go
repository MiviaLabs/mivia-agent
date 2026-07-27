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
