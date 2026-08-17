package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDeriveTitleFits pins the common case: base+" "+affix within maxRunes
// returns the untruncated concatenation.
func TestDeriveTitleFits(t *testing.T) {
	title := deriveTitle("feat(agent): chunk three", "[stack 3/12]", MaxTitleRunes)
	want := "feat(agent): chunk three [stack 3/12]"
	if title != want {
		t.Fatalf("title = %q, want %q", title, want)
	}
}

// TestDeriveTitleEmptyBase pins the base=="" fallback: the title is just the
// (possibly truncated) affix, with no leading separator space.
func TestDeriveTitleEmptyBase(t *testing.T) {
	title := deriveTitle("   ", "[stack 1/1]", MaxTitleRunes)
	if title != "[stack 1/1]" {
		t.Fatalf("title = %q, want the bare affix with no leading space", title)
	}
}

// TestDeriveTitleAffixAloneExceedsLimit pins the degenerate room<=0 branch:
// even the affix by itself is at or over maxRunes, so the base is dropped
// entirely and the (truncated) affix is returned - never a base+space
// fragment with no room for any of the affix.
func TestDeriveTitleAffixAloneExceedsLimit(t *testing.T) {
	affix := strings.Repeat("x", 10)
	title := deriveTitle("some base title", affix, 5)
	if title != "xxxxx" {
		t.Fatalf("title = %q, want the affix truncated to maxRunes with the base entirely dropped", title)
	}
}

// TestDeriveTitleNeverExceedsMaxRunes fuzzes the truncation guarantee across
// UTF-8 base titles (multi-byte runes: emoji, CJK, accented Latin) to pin
// that truncateRunes never splits a multi-byte sequence and the result never
// exceeds maxRunes, regardless of script.
func TestDeriveTitleNeverExceedsMaxRunes(t *testing.T) {
	affix := "[split 2/2, base: #142]"
	for _, base := range []string{
		strings.Repeat("é", 300),    // accented Latin, 2 bytes/rune
		strings.Repeat("中", 300),    // CJK, 3 bytes/rune
		strings.Repeat("🚀", 300),    // emoji, 4 bytes/rune
		strings.Repeat("a🚀é中", 100), // mixed widths
	} {
		title := deriveTitle(base, affix, MaxTitleRunes)
		if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
			t.Fatalf("title is %d runes, want <= %d; title=%q", n, MaxTitleRunes, title)
		}
		if !utf8.ValidString(title) {
			t.Fatalf("title is not valid UTF-8 (a multi-byte rune was split): %q", title)
		}
	}
}

// TestDeriveTitleKeepsAffixIntactWhenTruncating pins that the affix - the
// part carrying the merge-order/relationship information ("[stack 2/3]",
// "[split 2/2, base: #142]") - always survives a truncation in full; only
// the base (the human-authored words) is ever shortened. A truncated affix
// would silently lose the one piece of information a reviewer needs to
// understand why this PR exists.
func TestDeriveTitleKeepsAffixIntactWhenTruncating(t *testing.T) {
	affix := "[split 2/2, base: #142]"
	title := deriveTitle(strings.Repeat("a", 300), affix, MaxTitleRunes)
	if !strings.HasSuffix(title, affix) {
		t.Fatalf("title = %q, want it to end with the full, untruncated affix %q", title, affix)
	}
}
