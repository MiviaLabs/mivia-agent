package memory

import "strings"

// similarityMergeThreshold is the Jaccard similarity above which two entries
// are treated as near-duplicates and merged instead of both being kept
// (decision 3). Tuned against the labeled fixture pairs in
// similarity_test.go, not derived analytically - the actual tuning
// mechanism is that test, not this constant in isolation.
const similarityMergeThreshold = 0.62

// EntrySimilarity returns the Jaccard similarity between two entries based on their
// normalized title and summary tokens.
func EntrySimilarity(a, b Entry) float64 {
	aTokens, _ := tokenize(a.Title + " " + a.Summary)
	bTokens, _ := tokenize(b.Title + " " + b.Summary)
	return jaccardSimilarity(aTokens, bTokens)
}

// EntriesMergeable reports whether incoming is similar enough to existing
// that a caller should merge it in rather than keep it as its own entry.
// Jaccard token similarity alone is not a safe merge trigger: two short,
// distinct memories about different features (a login-form bug and an
// unrelated signup-form bug, say) routinely share almost every
// implementation word - form, submit, spinner, redirect, validation - while
// recording opposite outcomes about two different specific problems. Entry
// has no identity field to tell them apart, but it does have Verdict, and a
// verdict mismatch is the cheapest signal already on the model for "these
// are not a resave of the same finding": an entry recording something that
// worked (good) and one recording something that failed (bad) are never the
// same duplicate, whatever their vocabulary overlap.
//
// The check only fires when BOTH sides carry an assertive verdict (good,
// bad, or mixed). An empty verdict has nothing to disagree on, and neutral
// is not itself an assertive verdict: it is the fallback Scan assigns to a
// hand-authored or legacy file that never recorded a verdict at all
// (markdown_source.go Scan, parseProtocolMemory), so a "neutral" existing
// entry carries the same "unknown, not yet assessed" meaning as an empty
// one and must not block a merge with an incoming entry that does have a
// real assessment.
func EntriesMergeable(existing, incoming Entry) bool {
	if EntrySimilarity(existing, incoming) < similarityMergeThreshold {
		return false
	}
	if isAssertiveVerdict(existing.Verdict) && isAssertiveVerdict(incoming.Verdict) && existing.Verdict != incoming.Verdict {
		return false
	}
	return true
}

// isAssertiveVerdict reports whether v is an actual assessment (good, bad,
// or mixed) rather than "no assessment recorded" (empty, or neutral - the
// fallback Scan assigns when a file never carried a verdict at all).
func isAssertiveVerdict(v Verdict) bool {
	return v == VerdictGood || v == VerdictBad || v == VerdictMixed
}

// MergeEntries merges an incoming entry into an existing entry.
// It preserves the existing entry's identity (Title, Created, Scope),
// takes the richer/updated Summary and Why, merges unique Tags and References,
// and adopts the higher Importance. It returns the merged entry and a slice
// of collection names ("tags", "related", "references") that were truncated
// due to exceeding their capacity limits.
//
// Callers are expected to gate the call with EntriesMergeable first: this
// function still overwrites Summary/Good/Bad/Verdict unconditionally when
// incoming carries them, which is exactly the desired behavior for a
// confirmed near-duplicate resave (a later save with a richer or corrected
// account of the same finding) but would silently discard a distinct
// existing verdict or content on two entries that only look similar by
// vocabulary. That distinctness check belongs in EntriesMergeable, not here,
// so this function's contract - take incoming's non-empty fields - stays a
// single, predictable rule instead of conditionally guessing which
// overwrite is safe.
func MergeEntries(existing, incoming Entry) (Entry, []string) {
	res := existing
	if incoming.Created != "" && incoming.Created > res.Created {
		res.Created = incoming.Created
	}
	if incoming.Summary != "" {
		res.Summary = incoming.Summary
	}
	if incoming.Why != "" {
		if existing.Why != "" && existing.Why != incoming.Why && !strings.Contains(incoming.Why, existing.Why) {
			res.Why = incoming.Why + "\n\n## Prior context\n" + existing.Why
		} else {
			res.Why = incoming.Why
		}
	}
	if incoming.Verdict != "" && verdictOverridable(existing, incoming) {
		res.Verdict = incoming.Verdict
	}
	if incoming.Good != "" && verdictOverridable(existing, incoming) {
		res.Good = incoming.Good
	}
	if incoming.Bad != "" && verdictOverridable(existing, incoming) {
		res.Bad = incoming.Bad
	}
	if importanceRank(incoming.Importance) > importanceRank(existing.Importance) {
		res.Importance = incoming.Importance
	}

	var truncated []string
	var tagsTrunc, relTrunc, refTrunc bool
	res.Tags, tagsTrunc = mergeUniqueStrings(existing.Tags, incoming.Tags, MaxTags)
	if tagsTrunc {
		truncated = append(truncated, "tags")
	}
	res.Related, relTrunc = mergeUniqueStrings(existing.Related, incoming.Related, MaxRelated)
	if relTrunc {
		truncated = append(truncated, "related")
	}
	res.References, refTrunc = mergeUniqueStrings(existing.References, incoming.References, MaxReferences)
	if refTrunc {
		truncated = append(truncated, "references")
	}
	return res, truncated
}

// verdictOverridable reports whether incoming may replace existing's
// assessment fields (Verdict/Good/Bad). An unassessed incoming save (empty or
// neutral verdict - the tool layer defaults neutral) must not clobber an
// assertive verdict already recorded on the existing entry; EntriesMergeable
// already treats neutral the same way on the merge-decision side.
func verdictOverridable(existing, incoming Entry) bool {
	return !isAssertiveVerdict(existing.Verdict) || isAssertiveVerdict(incoming.Verdict)
}

// mergeUniqueStrings merges two string slices, deduplicating elements while
// preserving order. Existing-first ordering is used: existing items take
// precedence and appear first to maintain stable categorization of the
// canonical record; incoming items fill remaining capacity. If the union of
// unique non-empty items exceeds limit (when limit > 0), incoming-only items
// (or existing items beyond limit) are dropped and truncated is reported as true.
func mergeUniqueStrings(a, b []string, limit int) ([]string, bool) {
	seen := make(map[string]struct{}, len(a)+len(b))
	var out []string
	truncated := false
	add := func(items []string) {
		for _, s := range items {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, exists := seen[s]; !exists {
				seen[s] = struct{}{}
				if limit <= 0 || len(out) < limit {
					out = append(out, s)
				} else {
					truncated = true
				}
			}
		}
	}
	add(a)
	add(b)
	return out, truncated
}

func importanceRank(imp Importance) int {
	switch imp {
	case ImportanceHigh:
		return 3
	case ImportanceMedium:
		return 2
	case ImportanceLow:
		return 1
	default:
		return 0
	}
}

// jaccardSimilarity returns the Jaccard index of two token sets: the size of
// their intersection over the size of their union, in [0, 1]. Either input
// empty returns 0 (no similarity, not a division-by-zero panic).
//
// Reuses tokenize's normalization (lowercase, stopword-free, deduplicated
// Unicode letter/digit tokens) rather than a second normalization path, so
// "similar enough to merge" and "similar enough to match a search query" use
// the same notion of a token.
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, tok := range a {
		setA[tok] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, tok := range b {
		setB[tok] = struct{}{}
	}
	intersection := 0
	for tok := range setA {
		if _, ok := setB[tok]; ok {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
