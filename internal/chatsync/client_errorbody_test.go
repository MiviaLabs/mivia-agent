package chatsync

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestErrorBodySnippetPicksTextByBodyState pins the three input states the
// fallback must tell apart: no body and no read error gets the plain wording,
// a failed read gets the wording plus the read error's text, and a readable
// body gets the snippet path even when the read ended in an error too.
func TestErrorBodySnippetPicksTextByBodyState(t *testing.T) {
	plain := "the response body was empty or unreadable"

	if got := errorBodySnippet(nil, nil); got != plain {
		t.Errorf("errorBodySnippet(nil, nil) = %q, want %q", got, plain)
	}

	readErr := errors.New("connection reset mid-body")
	got := errorBodySnippet(nil, readErr)
	if !strings.Contains(got, "empty or unreadable") || !strings.Contains(got, "connection reset mid-body") {
		t.Errorf("errorBodySnippet(nil, err) = %q, want the wording WITH the read error text", got)
	}

	got = errorBodySnippet([]byte("hello"), readErr)
	if got != "hello" {
		t.Errorf("errorBodySnippet(body, err) = %q, want the body snippet, not the empty wording", got)
	}
}

// TestErrorBodySnippetCapHoldsAfterSanitization pins the order of
// operations: sanitization expands each invalid byte into a three-byte
// replacement rune, so capping before sanitizing could ship a snippet past
// the limit. Mid-string invalid bytes with a valid trailing rune are the
// shape that breaks a cap-first order.
func TestErrorBodySnippetCapHoldsAfterSanitization(t *testing.T) {
	body := append(bytes.Repeat([]byte{0xff}, 100), []byte(strings.Repeat("a", 500))...)
	got := errorBodySnippet(body, nil)
	if len(got) > errorBodySnippetLimit {
		t.Errorf("snippet is %d bytes, want at most %d", len(got), errorBodySnippetLimit)
	}
	if !strings.HasSuffix(got, "a") {
		t.Errorf("snippet %q does not end at the cut boundary", got)
	}
}

// TestErrorBodySnippetAllInvalidTextStillNamesTheGap pins the all-invalid
// body: a blob the cap and the sanitizer strip to nothing must produce the
// explicit empty-body wording, not an empty message an operator cannot act on.
func TestErrorBodySnippetAllInvalidTextStillNamesTheGap(t *testing.T) {
	blob := bytes.Repeat([]byte{0xff}, errorBodySnippetLimit+100)
	if got := errorBodySnippet(blob, nil); got != "the response body was empty or unreadable" {
		t.Errorf("all-invalid body = %q, want the explicit empty-body wording", got)
	}
	readErr := errors.New("connection reset mid-body")
	got := errorBodySnippet(blob, readErr)
	if !strings.Contains(got, "empty or unreadable") || !strings.Contains(got, "connection reset mid-body") {
		t.Errorf("all-invalid body with read error = %q, want the wording WITH the read error text", got)
	}
}

// TestTruncateRuneBoundaryPinsTheCut pins the cut's three decisions: an ASCII
// overrun cuts at exactly max, a partial multi-byte rune at the cut is trimmed
// rather than shipped broken, and an input already within max is returned
// unchanged - even when it ends in a lone continuation byte, because the trim
// loop is only for inputs the cut created.
func TestTruncateRuneBoundaryPinsTheCut(t *testing.T) {
	t.Run("ascii cuts at max", func(t *testing.T) {
		got := truncateRuneBoundary("abcdef", 4)
		if got != "abcd" {
			t.Errorf("= %q, want %q", got, "abcd")
		}
	})

	t.Run("straddled rune trimmed", func(t *testing.T) {
		// "éé" is 4 bytes; a cut at 3 leaves the lead byte of the second rune.
		got := truncateRuneBoundary("éé", 3)
		if got != "é" {
			t.Errorf("= %q, want the first rune only", got)
		}
	})

	t.Run("incomplete tail trimmed", func(t *testing.T) {
		// A cut that lands on a lead byte: the trim loop must not ship the
		// broken partial rune the cut created.
		got := truncateRuneBoundary("ab\xc3\xc3", 3)
		if got != "ab" {
			t.Errorf("= %q, want the partial rune dropped", got)
		}
	})

	t.Run("len equal to max unchanged", func(t *testing.T) {
		// Within budget means untouched, even with a lone continuation byte at
		// the end - the trim loop must not shorten an input it did not cut.
		in := "\x80\x80\x80"
		if got := truncateRuneBoundary(in, 3); got != in {
			t.Errorf("= %q, want %q unchanged", got, in)
		}
	})
}

// TestIsControlRuneTable pins the boundary: C0 controls and DEL are controls,
// space and printable text are not.
func TestIsControlRuneTable(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
	}{
		{0x00, true},
		{0x1f, true},
		{0x20, false},
		{0x7f, true},
		{'a', false},
	}
	for _, c := range cases {
		if got := isControlRune(c.r); got != c.want {
			t.Errorf("isControlRune(%#x) = %v, want %v", c.r, got, c.want)
		}
	}
}

// TestSanitizeErrorSnippetKeepsPrintableText pins the sanitizer end to end:
// NUL, bell and escape go, spaces and printable text stay, and clean text
// comes back unchanged.
func TestSanitizeErrorSnippetKeepsPrintableText(t *testing.T) {
	got := sanitizeErrorSnippet("a\x00b\x07c\x1bd e")
	if want := "abcd e"; got != want {
		t.Errorf("= %q, want %q", got, want)
	}
	if got := sanitizeErrorSnippet("clean text 123"); got != "clean text 123" {
		t.Errorf("clean text changed: %q", got)
	}
}
