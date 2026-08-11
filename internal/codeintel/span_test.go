package codeintel

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCollapseLineKeepsValidUTF8OnTruncation is the DC-6 regression for
// collapseLine: an over-cap one-line signature must be cut at a rune
// boundary, never inside a multi-byte character. The tools layer marshals
// Symbol.Signature and Definition.Signature through encoding/json, which
// silently replaces a partial rune with U+FFFD - so a raw byte cut makes the
// model read a mangled identifier instead of a clean rune-boundary cut.
func TestCollapseLineKeepsValidUTF8OnTruncation(t *testing.T) {
	// (a) Over-cap input whose multi-byte runes straddle byte 200. After
	// Fields+Join the string is "type X" + 70 x U+754C "界" + " struct{ A int }";
	// the 200-byte cut lands inside a 3-byte rune, so the raw s[:200] slice
	// ends with a partial 0xE7 0x95 sequence that TrimSpace cannot drop - the
	// current code returns invalid UTF-8 here.
	over := "type X" + strings.Repeat("\u754c", 70) + " struct{ A int }"
	if len(over) <= 200 {
		t.Fatalf("fixture must exceed the 200-byte cap, got %d bytes", len(over))
	}
	got := collapseLine(over)
	if !utf8.ValidString(got) {
		t.Fatalf("collapseLine on over-cap CJK input returned invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, " …") {
		t.Errorf("truncated signature must carry the ellipsis marker, got %q", got)
	}

	// (b)-(e) Boundary: inputs at or under the cap pass through unchanged
	// (0, max-1, and exactly max are the DC-6 boundary probes).
	for _, in := range []string{
		"",                       // 0 bytes
		"a",                      // 1 byte
		strings.Repeat("a", 199), // max-1
		strings.Repeat("a", 200), // exactly max
	} {
		if out := collapseLine(in); out != in {
			t.Errorf("collapseLine(%d bytes) = %q, want input unchanged", len(in), out)
		}
	}

	// (f) Negative path: over-cap ASCII-only input is truncated, never
	// returned whole, with a <=200-byte prefix and the marker present.
	ascii := strings.Repeat("a", 201)
	out := collapseLine(ascii)
	if out == ascii {
		t.Fatalf("over-cap input returned whole: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("ASCII truncation is invalid UTF-8: %q", out)
	}
	if !strings.HasSuffix(out, " …") {
		t.Errorf("truncated signature must carry the ellipsis marker, got %q", out)
	}
	if prefix := strings.TrimSuffix(out, " …"); len(prefix) > 200 {
		t.Errorf("truncated prefix is %d bytes, want <= 200", len(prefix))
	}

	// (g) End to end: a real .go file whose rendered type signature exceeds
	// the cap and contains multi-byte identifiers. Every emitted Signature
	// must be valid UTF-8 and bounded.
	path := filepath.Join(t.TempDir(), "wide.go")
	write(t, path, "package wide\n\ntype "+strings.Repeat("\u754c", 70)+" struct{ A int }\n")
	res, err := FileOutline(path)
	if err != nil {
		t.Fatalf("FileOutline failed: %v", err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("outline returned no symbols")
	}
	for _, s := range res.Symbols {
		if !utf8.ValidString(s.Signature) {
			t.Errorf("symbol %q signature is invalid UTF-8: %q", s.Name, s.Signature)
		}
		if strings.HasSuffix(s.Signature, " …") && len(s.Signature) > 204 {
			t.Errorf("symbol %q signature is %d bytes, want <= 204", s.Name, len(s.Signature))
		}
	}
}

// FuzzCollapseLineValidUTF8 checks the DC-6 property on arbitrary valid
// input: collapseLine always returns valid UTF-8, and when it had to
// truncate, the prefix before the ellipsis marker is at most the 200-byte cap
// (so the whole result is at most 204 bytes). The input domain is rendered
// source text, which is always valid UTF-8, so inputs that are themselves
// invalid are outside contract and skipped.
func FuzzCollapseLineValidUTF8(f *testing.F) {
	f.Add("")
	f.Add(strings.Repeat("a", 200))                                                                         // exactly max, ASCII
	f.Add(strings.Repeat("a", 201))                                                                         // over max, ASCII
	f.Add("type X" + strings.Repeat("\u754c", 70) + " struct{ A int }")                                     // over max, CJK
	f.Add("type X" + strings.Repeat("\u754c", 65) + "x" + strings.Repeat("\u754c", 5) + " struct{ A int }") // straddling
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		out := collapseLine(s)
		if !utf8.ValidString(out) {
			t.Fatalf("collapseLine(%q) = %q is invalid UTF-8", s, out)
		}
		if strings.HasSuffix(out, " …") {
			prefix := strings.TrimSuffix(out, " …")
			if len(prefix) > 200 {
				t.Fatalf("truncated prefix is %d bytes, want <= 200 (input %d bytes)", len(prefix), len(s))
			}
			if len(out) > 204 {
				t.Fatalf("truncated result is %d bytes, want <= 204", len(out))
			}
		}
	})
}
