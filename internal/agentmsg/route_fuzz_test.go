package agentmsg

import (
	"strings"
	"testing"
)

// FuzzParseAllowPairsNeverPanics asserts parseAllowPairs never panics on any
// input. Allow entries come from operator config and cross the trust boundary
// unchanged (internal/config/load.go fills numeric defaults only and never
// validates the entries), so a panic on hostile input would be a reachable
// defect regardless of whether the shape is valid.
func FuzzParseAllowPairsNeverPanics(f *testing.F) {
	seedAllowCorpus(f)
	f.Fuzz(func(t *testing.T, s string) {
		// A single entry must never panic.
		_ = parseAllowPairs([]string{s})
		// A newline-split view lets a seed carry several entries at once.
		_ = parseAllowPairs(strings.Split(s, "\n"))
	})
}

// seedAllowCorpus primes the target with the structured-input classes the
// regression tests name: empty, malformed, oversized, whitespace, and
// duplicate entries.
func seedAllowCorpus(f *testing.F) {
	// Empty entry.
	f.Add("")
	// A valid pair.
	f.Add("a->b")
	// A double dash still contains the separator. The pair splits wrongly.
	f.Add("a-->b")
	// A missing target.
	f.Add("a->")
	// A missing source.
	f.Add("->b")
	// An extra arrow segment.
	f.Add("a->b->c")
	// Surrounding whitespace.
	f.Add(" a -> b ")
	// An oversized entry.
	f.Add(strings.Repeat("x", 4096))
	// Duplicate pairs. The newline split makes two entries.
	f.Add("a->b\na->b")
}
