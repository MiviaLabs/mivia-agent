package contentref

import (
	"strings"
	"testing"
)

// FuzzParseNeverPanics asserts Parse never panics on any input. References
// cross the trust boundary (they arrive via cli/read_output, agentmsg
// validateRef, and ledger ParseReference re-exports), so a panic on hostile
// input would be a reachable defect regardless of whether the shape is valid.
func FuzzParseNeverPanics(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _ = Parse(s) // must never panic
	})
}

// FuzzParseRejectsNonCanonical asserts Parse is sound and complete:
//   - (soundness) when Parse accepts s, s must have exactly the canonical
//     shape "ref:<known kind>:<64 lowercase hex>";
//   - (completeness) when s has that canonical shape, Parse must accept it
//     and return the split kind and digest.
func FuzzParseRejectsNonCanonical(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, s string) {
		kind, digest, err := Parse(s)
		parts := strings.Split(s, ":")
		canonical := len(parts) == 3 && parts[0] == "ref" &&
			knownKind(parts[1]) && isLowerHexDigest(parts[2])
		if canonical && err != nil {
			t.Fatalf("Parse rejected canonical ref %q: %v", s, err)
		}
		if err == nil {
			if !canonical {
				t.Fatalf("Parse accepted non-canonical ref %q", s)
			}
			if kind != parts[1] || digest != parts[2] {
				t.Fatalf("Parse(%q) = (%q, %q), want (%q, %q)", s, kind, digest, parts[1], parts[2])
			}
		}
	})
}

// seedCorpus primes both targets with the structured-input classes the
// regression tests name: empty, truncated-to-16-hex, 63/65-hex, oversized,
// uppercase hex, non-hex, unknown kind, surrounding whitespace, an extra
// colon segment, and canonical refs for every known kind.
func seedCorpus(f *testing.F) {
	hex64 := strings.Repeat("a", 64)
	f.Add("")
	f.Add("ref:output:")
	f.Add("ref:output:" + strings.Repeat("a", 16)) // historical 8-byte truncation
	f.Add("ref:output:" + strings.Repeat("a", 63))
	f.Add("ref:output:" + strings.Repeat("a", 65))
	f.Add("ref:output:" + strings.Repeat("a", 4096))
	f.Add("ref:output:" + strings.Repeat("A", 64)) // uppercase hex
	f.Add("ref:output:" + strings.Repeat("g", 64)) // non-hex
	f.Add("ref:sha256:" + hex64)                   // unknown kind
	f.Add(" ref:output:" + hex64)                  // leading whitespace
	f.Add("ref:output:" + hex64 + " ")             // trailing whitespace
	f.Add("ref:output:" + hex64 + ":x")            // extra colon segment
	f.Add("ref:output:" + hex64)                   // canonical output
	f.Add("ref:error:" + hex64)                    // canonical error
	f.Add("ref:message:" + hex64)                  // canonical message
}
