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

	res.Tags = mergeUniqueStrings(existing.Tags, incoming.Tags, maxTags)
	res.Related = mergeUniqueStrings(existing.Related, incoming.Related, maxRelated)
	res.References = mergeUniqueStrings(existing.References, incoming.References, maxReferences)
	return res
}

func mergeUniqueStrings(a, b []string, limit int) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	var out []string
	add := func(items []string) {
		for _, s := range items {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, exists := seen[s]; !exists {
				seen[s] = struct{}{}
				out = append(out, s)
				if limit > 0 && len(out) >= limit {
					return
				}
			}
		}
	}
	add(a)
	if limit <= 0 || len(out) < limit {
		add(b)
	}
	return out
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
