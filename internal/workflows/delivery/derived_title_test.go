package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDeriveTitleFits pins the common case: base+" "+affix within maxRunes
// returns the untruncated concatenation, fits=true, and fullRunes equal to
// the actual result's rune count.
func TestDeriveTitleFits(t *testing.T) {
	title, fits, fullRunes := deriveTitle("feat(agent): chunk three", "[stack 3/12]", MaxTitleRunes)
	want := "feat(agent): chunk three [stack 3/12]"
	if title != want {
		t.Fatalf("title = %q, want %q", title, want)
	}
	if !fits {
		t.Fatal("fits = false, want true")
	}
	if fullRunes != utf8.RuneCountInString(want) {
		t.Fatalf("fullRunes = %d, want %d (the actual result's rune count)", fullRunes, utf8.RuneCountInString(want))
	}
}

// TestDeriveTitleReportsFullRunesConsistently pins the bug an adversarial
// review caught: a caller that recomputed the overflow rune count from the
// RAW, untrimmed baseTitle (instead of using deriveTitle's own fullRunes)
// would diverge whenever baseTitle carries whitespace deriveTitle trims -
// misleading a repair agent about the real overflow amount. fullRunes must
// always equal the trimmed-base-plus-affix candidate deriveTitle itself
// measured, never the raw input's length.
func TestDeriveTitleReportsFullRunesConsistently(t *testing.T) {
	rawTitle := strings.Repeat("a", 300) + "   " // 3 trailing spaces deriveTitle trims
	affix := "[stack 2/3]"
	_, fits, fullRunes := deriveTitle(rawTitle, affix, MaxTitleRunes)
	if fits {
		t.Fatal("fits = true, want false (300 a's alone already exceeds MaxTitleRunes)")
	}
	// What a caller recomputing from the RAW input would get (the bug):
	misleading := utf8.RuneCountInString(rawTitle) + utf8.RuneCountInString(affix) + 1
	// What deriveTitle itself measured (the fix): trimmed base + " " + affix.
	trimmedBase := strings.TrimRight(strings.TrimSpace(rawTitle), " ")
	want := utf8.RuneCountInString(trimmedBase) + 1 + utf8.RuneCountInString(affix)
	if fullRunes != want {
		t.Fatalf("fullRunes = %d, want %d (the trimmed measurement)", fullRunes, want)
	}
	if fullRunes == misleading {
		t.Fatalf("fullRunes = %d equals the RAW-input recompute %d; the whitespace-trim divergence this test guards against is not actually exercised by this fixture", fullRunes, misleading)
	}
}

// TestDeriveTitleEmptyBase pins the base=="" fallback: the title is just the
// (possibly truncated) affix, with no leading separator space.
func TestDeriveTitleEmptyBase(t *testing.T) {
	title, fits, fullRunes := deriveTitle("   ", "[stack 1/1]", MaxTitleRunes)
	if title != "[stack 1/1]" {
		t.Fatalf("title = %q, want the bare affix with no leading space", title)
	}
	if !fits {
		t.Fatal("fits = false, want true (a short affix alone fits easily)")
	}
	if fullRunes != utf8.RuneCountInString("[stack 1/1]") {
		t.Fatalf("fullRunes = %d, want %d", fullRunes, utf8.RuneCountInString("[stack 1/1]"))
	}
}

// TestDeriveTitleAffixAloneExceedsLimit pins the degenerate room<=0 branch:
// even the affix by itself is at or over maxRunes, so the base is dropped
// entirely and the (truncated) affix is returned - never a base+space
// fragment with no room for any of the affix.
func TestDeriveTitleAffixAloneExceedsLimit(t *testing.T) {
	affix := strings.Repeat("x", 10)
	title, fits, _ := deriveTitle("some base title", affix, 5)
	if fits {
		t.Fatal("fits = true, want false")
	}
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
		title, fits, _ := deriveTitle(base, affix, MaxTitleRunes)
		if fits {
			t.Fatalf("base %q (300+ runes) unexpectedly fit within %d", base[:10]+"...", MaxTitleRunes)
		}
		if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
			t.Fatalf("title is %d runes, want <= %d; title=%q", n, MaxTitleRunes, title)
		}
		if !utf8.ValidString(title) {
			t.Fatalf("title is not valid UTF-8 (a multi-byte rune was split): %q", title)
		}
	}
}
