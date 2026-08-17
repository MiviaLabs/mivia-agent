package textutil

import (
	"strings"
	"testing"
)

func TestHasControlByteRejectsC0AndDEL(t *testing.T) {
	for _, s := range []string{"a\x00b", "a\x1eb", "a\x7fb", "a\tb", "a\nb", "\x01"} {
		if !HasControlByte(s) {
			t.Fatalf("HasControlByte(%q) = false, want true", s)
		}
	}
}

func TestHasControlByteAcceptsOrdinaryText(t *testing.T) {
	for _, s := range []string{"", "hello", "a b c", "unicode-é-safe", "sp ace\x20end"} {
		if HasControlByte(s) {
			t.Fatalf("HasControlByte(%q) = true, want false", s)
		}
	}
}

// TestHasControlByteByteSweep pins the exact C0+DEL boundary over the whole
// byte domain: every byte below 0x20 and 0x7F must be rejected, and every
// other byte (including the C1 range 0x80-0x9F, continuation bytes, and 0xFF)
// must be accepted. A regression that widens or narrows the boundary by even
// one byte fails here.
func TestHasControlByteByteSweep(t *testing.T) {
	for b := 0; b <= 0xff; b++ {
		got := HasControlByte(string([]byte{byte(b)}))
		want := b < 0x20 || b == 0x7f
		if got != want {
			t.Errorf("HasControlByte(0x%02x) = %v, want %v", b, got, want)
		}
	}
}

// TestHasControlByteBoundaryPositions checks that detection is position
// independent: a control byte at the start, at the end, or as the entire
// string is found, while the adjacent safe bytes 0x20 and 0x7E never are.
func TestHasControlByteBoundaryPositions(t *testing.T) {
	for _, s := range []string{"\x00abc", "\x1fabc", "\x7fabc", "abc\x00", "abc\x1e", "abc\x7f", "\x00", "\x1e", "\x7f"} {
		if !HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"\x20abc", "\x7eabc", "abc\x20", "abc\x7e", "\x20", "\x7e"} {
		if HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = true, want false", s)
		}
	}
}

// TestHasControlByteMultiByteRuneContinuationBytes checks that valid UTF-8
// continuation bytes in the C1 range (0x80-0x9F) inside multi-byte runes are
// never mistaken for control bytes: C1 is not C0/DEL, and a C1 byte cannot
// appear as a standalone separator byte in valid UTF-8.
func TestHasControlByteMultiByteRuneContinuationBytes(t *testing.T) {
	for _, s := range []string{
		// 0xC3 0xA9 and 0xC3 0x89: both continuation bytes are in the C1 range.
		"\u00e9",
		"\u00c9",
		// 0xE6 0x97 0xA5 ...: all continuation bytes.
		"\u65e5\u672c\u8a9e",
		// 0xF0 0x9F 0x99 0x82: 0x9F is a C1-range byte.
		"\U0001f642",
		// 0xE2 0x80 0xA6: the ellipsis rune carries 0x80.
		"\u2026",
		// Rune in the middle.
		"a\u00e9b",
	} {
		if HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = true, want false", s)
		}
	}
}

// TestHasControlByteMalformedUTF8 checks that malformed UTF-8 with no C0/DEL
// byte is accepted, and a control byte anywhere in malformed input is found.
func TestHasControlByteMalformedUTF8(t *testing.T) {
	for _, s := range []string{"\x80\x80\x80", "\xff\xfe", "\xc3", "\x80abc\x80"} {
		if HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"\x80\x00\x80", "\xff\x1e\xfe", "\xc3\x7f"} {
		if !HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = false, want true", s)
		}
	}
}

// TestHasControlByteOversizedInput checks a linear scan over a 1 MiB input:
// no false positive, and a single control byte at the very end or in the
// middle is still found (the scan must run to completion, not bail out early).
func TestHasControlByteOversizedInput(t *testing.T) {
	safe := strings.Repeat("a", 1<<20)
	if HasControlByte(safe) {
		t.Error("HasControlByte(1 MiB safe) = true, want false")
	}
	if !HasControlByte(safe + "\x7f") {
		t.Error("HasControlByte(1 MiB + DEL) = false, want true")
	}
	if !HasControlByte(safe[:1<<19] + "\x1e" + safe[1<<19:]) {
		t.Error("HasControlByte(1 MiB with 0x1e in the middle) = false, want true")
	}
}

// TestHasControlByteDigestSeparatorProtection ties the check to its callers'
// purpose: sourceKeyDigest (internal/workflows/controller) separates member
// and finding IDs with 0x00 and 0x1e, so a finding or member ID smuggling
// either byte would collide two canonical source keys onto one digest. The
// exact collision example from the decode path ("X\x1esecurity\x00Y") must be
// rejected.
func TestHasControlByteDigestSeparatorProtection(t *testing.T) {
	for _, s := range []string{"\x00", "\x1e", "X\x1esecurity\x00Y", "a\x00", "\x1eb"} {
		if !HasControlByte(s) {
			t.Errorf("HasControlByte(%q) = false, want true", s)
		}
	}
}

// FuzzHasControlByte pins the C0+DEL contract over arbitrary multi-byte
// input: the result must agree byte-for-byte with an independent reference
// scan, so no byte in or out of the control range can ever be misclassified.
func FuzzHasControlByte(f *testing.F) {
	for _, seed := range []string{
		"", "hello", "a b c", "unicode-\u00e9-safe", "\x00", "\x1e", "\x7f", "\x1f", "\x20",
		"a\x00b", "a\x1eb", "a\x7fb", "\u65e5\u672c\u8a9e", "\U0001f642", "\xff\xfe", "\x80\x80\x80",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := HasControlByte(s), referenceHasControlByte(s); got != want {
			t.Fatalf("HasControlByte(%q) = %v, want %v", s, got, want)
		}
	})
}

// referenceHasControlByte is an independent oracle for the C0+DEL contract:
// it builds a lookup table from the two explicit ranges (0x00-0x1F and 0x7F)
// and consults it per byte, so a regression that moves either boundary in the
// implementation is detected by the disagreement.
func referenceHasControlByte(s string) bool {
	var tbl [256]bool
	for b := 0; b < 0x20; b++ {
		tbl[b] = true
	}
	tbl[0x7f] = true
	for i := 0; i < len(s); i++ {
		if tbl[s[i]] {
			return true
		}
	}
	return false
}
