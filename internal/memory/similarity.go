package memory

// similarityMergeThreshold is the Jaccard similarity above which two entries
// are treated as near-duplicates and merged instead of both being kept
// (decision 3). Tuned against the labeled fixture pairs in
// similarity_test.go, not derived analytically - the actual tuning
// mechanism is that test, not this constant in isolation.
const similarityMergeThreshold = 0.82

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
