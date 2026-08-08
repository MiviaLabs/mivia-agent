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
	f.Fuzz(func(t *testing.T, oldText, newText string, context int) {
		ctx := context%12 - 1 // -1..10
		r, err := Compute(oldText, newText, Options{})
		if err != nil {
			return
		}
		got := FormatUnifiedAt("x", r, 1, 1, ctx)
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
		}
	})
}
