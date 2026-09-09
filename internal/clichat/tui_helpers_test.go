package clichat

import "testing"

// TestStripANSIRemovesCSISequences pins the common case: a CSI colour
// sequence is removed and the surrounding text survives intact.
func TestStripANSIRemovesCSISequences(t *testing.T) {
	if got := StripANSI("\033[31mred\033[0m text"); got != "red text" {
		t.Fatalf("StripANSI = %q, want %q", got, "red text")
	}
}

// TestStripANSIConsumesNonCSIEscape covers the escape-without-bracket branch:
// after ESC the scanner waits for '[' and drops every rune until it arrives.
// An OSC sequence (ESC ']') therefore swallows the rest of the string, which
// is why callers must not feed OSC-bearing text to this stripper.
func TestStripANSIConsumesNonCSIEscape(t *testing.T) {
	if got := StripANSI("keep\033]0;title\007drop"); got != "keep" {
		t.Fatalf("StripANSI = %q, want %q (everything after a non-CSI escape is consumed)", got, "keep")
	}
	if got := StripANSI("keep\033"); got != "keep" {
		t.Fatalf("StripANSI = %q, want %q for a trailing bare escape", got, "keep")
	}
}
