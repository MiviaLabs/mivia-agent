package redact

import (
	"strings"
	"testing"
)

// Edges of the content-aware hold: a delta cut inside a rune, a rule that
// opens on an assertion, a complete match the automaton has finished with, and
// two threads meeting at one program counter. Each is a deterministic pin for
// a step of safeCut that only the random property test otherwise covers.

// TestADeltaEndingMidRuneHoldsTheRunesOpeningBytes: the first byte of KELVIN
// SIGN (U+212A, which (?i) folds to k) closes one delta and its last two open
// the next. Decoded alone the first byte is U+FFFD and matches nothing, so a
// hold that let it ship would then see `ey: hunter2` with no context and ship
// `api_Key: hunter2` in two clean halves. Provider JSON cannot end mid-rune;
// the primitive must not depend on that.
func TestADeltaEndingMidRuneHoldsTheRunesOpeningBytes(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	kelvin := "K"
	for _, fragments := range [][]string{
		{"use api_" + kelvin[:1], kelvin[1:] + "ey: hunter2 now"},
		{"the " + "ſ"[:1], "ſ"[1:] + "ecret: pw1 now"},
		{"TO" + "K"[:2], "K"[2:] + "en=abc now"},
	} {
		whole := strings.Join(fragments, "")
		want := Text(whole)
		if want == whole {
			t.Fatalf("the whole-text redaction leaves %q alone, so the split proves nothing", whole)
		}
		if got := pushAll(fragments); got != want {
			t.Errorf("fragments %q shipped %q, want %q", fragments, got, want)
		}
	}

	var s Stream
	if got := s.Push("hello \xe2"); got != "hello " {
		t.Errorf("a delta ending mid-rune shipped %q, want the prose and not the half rune", got)
	}
	if s.held != "\xe2" {
		t.Errorf("held %q, want the rune's opening byte", s.held)
	}
	if got := s.Push("\x84\xaa!"); got != "K!" {
		t.Errorf("the completed rune shipped as %q", got)
	}
}

// TestARuleOpeningOnAnAssertionStillHolds pins the epsilon edge through
// InstEmptyWidth in the automaton. A thread that stops at `\b` instead of
// passing it never reaches `key=`, nothing is held, and `key=` then `abc`
// ship in two clean halves.
func TestARuleOpeningOnAnAssertionStillHolds(t *testing.T) {
	withPolicy(t, []string{`\bkey=\S+`, `(?m)^pw:\S+`})

	for _, fragments := range [][]string{
		{"the ", "key=", "abc", " z"},
		{"line\n", "pw:", "abc", " z"},
	} {
		whole := strings.Join(fragments, "")
		want := Text(whole)
		if want == whole {
			t.Fatalf("the whole-text redaction leaves %q alone, so the split proves nothing", whole)
		}
		if got := pushAll(fragments); got != want {
			t.Errorf("fragments %q shipped %q, want %q", fragments, got, want)
		}
	}
}

// TestAnAssertionAtTheCutOverRedactsButNeverLeaks states the documented
// divergence (partial.go, chat-sync-wire.md): the redactor sees a boundary at
// the cut that the settled text does not have. The wire may carry MORE
// placeholder than Text(whole); it must never carry the value.
func TestAnAssertionAtTheCutOverRedactsButNeverLeaks(t *testing.T) {
	withPolicy(t, []string{`\bkey=\S+`})

	whole := "monkey=abc z"
	if Text(whole) != whole {
		t.Fatalf("the settled text %q is redacted; the divergence needs it left alone", whole)
	}
	got := pushAll([]string{"mon", "key=", "abc", " z"})
	if strings.Contains(got, "abc") && got != whole {
		t.Fatalf("shipped %q: neither the settled text nor a full redaction", got)
	}
	t.Logf("documented divergence: settled %q, streamed %q", whole, got)
}

// TestACompleteMatchUnderALaterOpenThreadIsNotCut pins rule 2 on its own.
// `abc123` is a complete match with no live thread - the pattern cannot
// consume more - while the second pattern's thread from `123` is still
// waiting for `x`. The earliest open offset is 3, inside the match; without
// the fixed-point loop the cut lands there and `abc` ships clean, then
// `123y` matches nothing.
func TestACompleteMatchUnderALaterOpenThreadIsNotCut(t *testing.T) {
	withPolicy(t, []string{`abc[0-9]{3}`, `[0-9]{3}x`})

	if got, want := pushAll([]string{"abc123", "y"}), Text("abc123y"); got != want {
		t.Fatalf("shipped %q, want %q", got, want)
	}
}

// TestTwoThreadsMeetingAtOneCounterKeepTheEarlierStart pins the merge rule
// in addThread. Both alternatives of `(?:a|xa)b` reach the same `b`
// instruction after `xa`: the thread from `a` (start 1) and the thread from
// `xa` (start 0). Keeping the later start ships `x` and holds `a`, and the
// wire reads `x[redacted]` where the settled text is `[redacted]`.
func TestTwoThreadsMeetingAtOneCounterKeepTheEarlierStart(t *testing.T) {
	withPolicy(t, []string{`(?:a|xa)b`})

	if got, want := pushAll([]string{"xa", "b"}), Text("xab"); got != want {
		t.Fatalf("shipped %q, want %q", got, want)
	}
}
