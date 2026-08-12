package diff

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzFormatUnifiedHunkHeaders checks that FormatUnifiedAt never panics on
// arbitrary input and that every emitted hunk header names a 1-based start
// with non-negative counts. It covers malformed and random input beyond the
// hand-written fixtures.
func FuzzFormatUnifiedHunkHeaders(f *testing.F) {
	f.Add("a\n1\n2\n3\n4\nb", "x\n1\n2\n3\n4\ny", 1)
	f.Add("a\nb\nc\nd\ne", "a\nX\nc\nd\ne", 3)
	f.Add("", "", 1)
	f.Add("a\nb\nc", "a\nb\nc", -1)
	f.Add("a", "b", 0)
	f.Add("line one\nline two\nline three", "line one\nchanged\nline three", 2)
	f.Add("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10", "l1\nl2\nl3\nl4\nchanged\nl6\nl7\nl8\nl9\nl10", 3)
	f.Add("a\nx\n", "a\nx", 1)
	f.Add("a\nx", "a\nx\n", 1)
	f.Add("a\n", "a\nb", 1)
	f.Add("a\nb\n", "a\nb\nc", 1)
	f.Fuzz(func(t *testing.T, oldText, newText string, context int) {
		ctx := context%12 - 1 // -1..10
		r, err := Compute(oldText, newText, Options{})
		if err != nil {
			return
		}
		// Honest stats: distinct inputs must never report +0 -0. splitLines is
		// injective and the trailing-newline strip never fires when it would
		// make the line arrays identical, so a non-equal pair always yields a
		// non-empty op sequence.
		if oldText != newText {
			ins, del := Stats(r)
			if ins == 0 && del == 0 {
				t.Fatalf("distinct inputs %q -> %q reported +0 -0", oldText, newText)
			}
		}
		got := FormatUnifiedAt("x", r, 1, 1, ctx)
		oldLineCount := len(strings.Split(oldText, "\n"))
		newLineCount := len(strings.Split(newText, "\n"))
		for _, line := range strings.Split(got, "\n") {
			if !strings.HasPrefix(line, "@@ ") {
				continue
			}
			var a, b, c, d int
			if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &a, &b, &c, &d); err != nil {
				t.Fatalf("unparseable hunk header %q: %v", line, err)
			}
			if a < 1 || c < 1 {
				t.Fatalf("hunk start below one: %q", line)
			}
			if b < 0 || d < 0 {
				t.Fatalf("negative hunk count: %q", line)
			}
			if a+b-1 > oldLineCount {
				t.Fatalf("old range %d..%d exceeds raw old line count %d: %q", a, a+b-1, oldLineCount, line)
			}
			if c+d-1 > newLineCount {
				t.Fatalf("new range %d..%d exceeds raw new line count %d: %q", c, c+d-1, newLineCount, line)
			}
		}
	})
}
