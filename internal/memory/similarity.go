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

// MergeEntries merges an incoming entry into an existing entry.
// It preserves the existing entry's identity (Title, Created, Scope),
// takes the richer/updated Summary and Why, merges unique Tags and References,
// and adopts the higher Importance.
func MergeEntries(existing, incoming Entry) Entry {
	res := existing
	if incoming.Summary != "" {
		res.Summary = incoming.Summary
	}
	if incoming.Why != "" {
		res.Why = incoming.Why
	}
	if incoming.Good != "" {
		res.Good = incoming.Good
	}
	if incoming.Bad != "" {
		res.Bad = incoming.Bad
	}
	if incoming.Verdict != "" {
		res.Verdict = incoming.Verdict
	}
	if importanceRank(incoming.Importance) > importanceRank(existing.Importance) {
		res.Importance = incoming.Importance
	}

	// Merge unique tags
	tagSet := make(map[string]struct{}, len(existing.Tags)+len(incoming.Tags))
	var mergedTags []string
	for _, t := range append(existing.Tags, incoming.Tags...) {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, seen := tagSet[t]; !seen {
			tagSet[t] = struct{}{}
			mergedTags = append(mergedTags, t)
		}
	}
	res.Tags = mergedTags

	// Merge unique related
	relSet := make(map[string]struct{}, len(existing.Related)+len(incoming.Related))
	var mergedRels []string
	for _, r := range append(existing.Related, incoming.Related...) {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, seen := relSet[r]; !seen {
			relSet[r] = struct{}{}
			mergedRels = append(mergedRels, r)
		}
	}
	res.Related = mergedRels

	// Merge unique references
	refSet := make(map[string]struct{}, len(existing.References)+len(incoming.References))
	var mergedRefs []string
	for _, r := range append(existing.References, incoming.References...) {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, seen := refSet[r]; !seen {
			refSet[r] = struct{}{}
			mergedRefs = append(mergedRefs, r)
		}
	}
	res.References = mergedRefs

	return res
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
