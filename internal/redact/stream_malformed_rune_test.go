package redact

import (
	"testing"
	"unicode/utf8"
)

// TestSafeCutBacksOffACutInsideAMalformedRune covers the byte-level
// backoff at the end of safeCut. The hold-back candidates ahead of it
// (incompleteRuneStart, a regexp match start) are rune-aligned only while
// the buffer is valid UTF-8. A provider that streams a truncated rune in
// the MIDDLE of a block - the opening byte of a three-byte rune followed
// by text that is not its continuation - makes each stray byte decode as
// one-byte U+FFFD, so a partial-match thread can start on a continuation
// byte and the cut lands inside the malformed sequence.
//
// Shipping that prefix would put invalid UTF-8 on the wire and draw a
// replacement character in the transcript before the consumer could
// reassemble the halves. The backoff must walk the cut back to the
// nearest rune start.
//
// The pattern is a synthetic fixture, not a shipped rule: its leading `.`
// is what lets a match begin on the stray byte, which is the condition
// under test. No real key material appears anywhere in this file.
func TestSafeCutBacksOffACutInsideAMalformedRune(t *testing.T) {
	p, err := Compile([]string{`.key-[a-z]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	// "ab" then the first two bytes of a three-byte rune, then 'k' - the
	// start of a name the pattern could still complete.
	const buf = "ab\xe4\xb8k"
	const strayByte = 3 // buf[3] == 0xb8, a continuation byte

	if utf8.RuneStart(buf[strayByte]) {
		t.Fatalf("fixture broken: buf[%d] must be a continuation byte", strayByte)
	}
	if open := p.partial[0].earliestOpen(buf); open != strayByte {
		t.Fatalf("earliestOpen = %d; the fixture must put the raw cut candidate on the stray byte (%d)", open, strayByte)
	}

	got := p.safeCut(buf)
	if got != 2 {
		t.Fatalf("safeCut = %d; want 2, the rune start below the stray byte at %d", got, strayByte)
	}
	if !utf8.RuneStart(buf[got]) {
		t.Fatalf("safeCut = %d, which is not a rune start: the shipped prefix splits a rune", got)
	}
	if !utf8.ValidString(buf[:got]) {
		t.Fatalf("shipped prefix %q is not valid UTF-8", buf[:got])
	}
}
