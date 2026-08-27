package remainder

// FuzzFitTruncation sweeps the pure byte-budget arithmetic of fitTruncation
// over arbitrary (body, total, maxBytes > 0, ref, trailer). The envelope
// invariants under test: the result always fits maxBytes; a "ref:" marker in
// the result implies the FULL ref was printed (a partial ref is never
// emitted); an empty ref never produces a "ref:" marker; and the result is
// valid UTF-8 after trimPartialRune.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzFitTruncation(f *testing.F) {
	ref := "ref:output:" + strings.Repeat("a", 64)
	body200 := strings.Repeat("x", 200)
	boundary := len(TruncationNotice(200, 200, ref))

	f.Add(body200, 200, boundary, ref, "")
	f.Add("", 0, len(TruncationNotice(0, 0, ref)), ref, "")
	f.Add(body200, 200, boundary-1, ref, "")
	f.Add(body200, 200, boundary+1, ref, "")
	f.Add(body200, 200, 1, "", "")
	f.Add("tiny", 4, 200, "", "")
	f.Add(strings.Repeat("héllo wörld ", 40), 400, 120, "", "")

	f.Fuzz(func(t *testing.T, body string, total, maxBytes int, ref, trailer string) {
		if maxBytes <= 0 {
			t.Skip()
		}
		// body and trailer are opaque content printed verbatim: a "ref:" in
		// either would mint the marker without fitTruncation's doing, and an
		// invalid-UTF-8 trailer (appended verbatim) would break UTF-8 validity
		// regardless of the arithmetic under test.
		if strings.Contains(body, "ref:") || strings.Contains(trailer, "ref:") {
			t.Skip()
		}
		if !utf8.ValidString(trailer) {
			t.Skip()
		}
		// ref is printed verbatim inside the notice, so an invalid-UTF-8 ref
		// would also break UTF-8 validity without exercising the fix. Real
		// callers only pass sdkadapter-minted ASCII refs.
		if !utf8.ValidString(ref) {
			t.Skip()
		}

		result := fitTruncation(body, total, maxBytes, ref, trailer)

		if len(result) > maxBytes {
			t.Fatalf("len(result)=%d > maxBytes=%d", len(result), maxBytes)
		}
		if strings.Contains(result, "ref:") {
			if ref == "" {
				t.Fatalf("ref marker emitted with an empty ref: %q", result)
			}
			if !strings.Contains(result, ref) {
				t.Fatalf("partial ref emitted: %q", result)
			}
		}
		if !utf8.ValidString(result) {
			t.Fatalf("result is not valid UTF-8: %q", result)
		}
	})
}
