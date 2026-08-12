package textutil

import "testing"

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
