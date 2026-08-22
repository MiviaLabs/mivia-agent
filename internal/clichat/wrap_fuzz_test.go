package clichat

// FuzzWrapANSIv2 sweeps wrapANSIv2, the TUI render path for plain and
// markdown text (reached via plainTextRenderer.RenderText and
// markdownRenderer.RenderText, and the tool-panel/renderer callers). It
// guards the ANSI scanner and the flushLine slice arithmetic against
// malformed and oversized input (invariant: no panic), pins the wrap contract
// (every breakable output line fits within the effective width, and a wide
// rune is never split across lines), and verifies that the whitespace-
// separated token stream survives the wrap in order (excluding rendered table
// rows, which deliberately hard-truncate and drop the tail). wrapANSIv2 is a
// pure string function with no I/O, so a deterministic fuzz target is
// practical.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzWrapANSIv2(f *testing.F) {
	seeds := []string{
		"",
		"hello world foo bar",
		"short",
		"\033[1mhello world foo bar\033[0m",
		"你好 世界 测试 宽度 对齐",
		"│ Key │ Behavior │ Notes │",
		"\033[32m✓\033[0m read_file 123ms \033[31m✗\033[0m failed 456ms",
		"a\tb\tc",
		"superlongwordthatdoesnotfit",
		"ab  \U00020000",
		"  leading and trailing  ",
		"\033[0m\033[1m\033[31m",
		"line one\nline two",
	}
	widths := []int{1, 5, 8, 40}
	for _, s := range seeds {
		for _, w := range widths {
			f.Add(s, w)
		}
	}

	f.Fuzz(func(t *testing.T, input string, maxWidth int) {
		// wrapANSIv2 clamps maxWidth below 5; test against the effective width.
		effective := maxWidth
		if effective < 5 {
			effective = 5
		}

		out := WrapANSIv2(input, maxWidth) // must not panic

		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			// A wide rune is never split: line boundaries only ever fall on
			// ASCII space bytes, so when the input is valid UTF-8 every output
			// line is complete UTF-8 too. (For invalid-UTF-8 inputs the wrap
			// copies raw bytes verbatim, so validity is not asserted there.)
			if utf8.ValidString(input) && !utf8.ValidString(line) {
				t.Fatalf("line %q is not valid UTF-8 (rune split) for input %q", line, input)
			}
			// Breakable lines (contain a space/tab, or are rendered table
			// rows) must fit within the effective width. Unbreakable words
			// pass through whole by contract and may exceed it.
			if (strings.ContainsAny(line, " \t") || isRenderedTableRow(line)) && VisibleWidth(line) > effective {
				t.Fatalf("breakable line %q has visible width %d > %d (input %q, maxWidth %d)",
					line, VisibleWidth(line), effective, input, maxWidth)
			}
		}

		// Rendered table rows hard-truncate (content after the cut is
		// deliberately dropped), so token preservation is checked only when
		// no input line is a rendered table row.
		if !containsTableRow(input) {
			want := strings.Fields(stripAnsiOut(input))
			got := strings.Fields(stripAnsiOut(out))
			if len(got) != len(want) {
				t.Fatalf("token count: got %d want %d (input %q)", len(got), len(want), input)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("token %d: got %q want %q (input %q)", i, got[i], want[i], input)
				}
			}
		}
	})
}

func containsTableRow(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if isRenderedTableRow(line) {
			return true
		}
	}
	return false
}
