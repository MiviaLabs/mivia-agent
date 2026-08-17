package cli

// FuzzHighlightTokensYAML sweeps the yaml token highlighter, the function
// behind the non-termination bug this change fixes. It guards the zero-width
// rule-match fix (invariants: no panic, no unbounded output growth) and pins
// content preservation for valid UTF-8 inputs without raw ESC bytes.
// highlightTokens is a pure byte-string function with no I/O, so a
// deterministic fuzz target is practical.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzHighlightTokensYAML(f *testing.F) {
	seeds := []string{
		"",
		"name: value",
		"- item",
		"'quoted'",
		"\"quoted\"",
		"key:",
		"# comment",
		"  indented: x",
		"1 1 1 1",
		strings.Repeat("a", 4096),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	def := langDefs["yaml"]

	f.Fuzz(func(t *testing.T, line string) {
		out := highlightTokens(line, def) // must terminate and not panic

		// Bounded growth: every input byte is emitted exactly once, wrapped in
		// at most one ANSI span (6-byte SGR color + 5-byte reset), so output is
		// at most 12*len(line); +64 absorbs the loop constants. (8x would be
		// too tight: single digits and single spaces are each wrapped, e.g.
		// "1 1 1 1" produces ~10x its input size.)
		if len(out) > 12*len(line)+64 {
			t.Fatalf("output grew to %d bytes for %d input bytes (unbounded growth?)", len(out), len(line))
		}

		// Content preservation only for valid UTF-8 without raw ESC bytes:
		// stripANSI (bubble_leftrail.go) re-encodes invalid UTF-8 as U+FFFD and
		// a raw ESC merges with the following ANSI span, so neither admits an
		// exact equality assertion.
		if !strings.ContainsRune(line, '\033') && utf8.ValidString(line) && stripANSI(out) != line {
			t.Fatalf("stripANSI(highlightTokens(%q)) = %q, want %q", line, stripANSI(out), line)
		}
	})
}
