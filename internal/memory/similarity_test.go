package memory

import "testing"

func TestJaccardSimilarityMergeThreshold(t *testing.T) {
	cases := []struct {
		name     string
		a, b     string
		wantHigh bool // true means similarity should be >= mergeThreshold (0.82)
	}{
		{
			name:     "near-identical rewording merges",
			a:        "deploy pipeline fix pinned runner image",
			b:        "deploy pipeline fix pinned the runner image",
			wantHigh: true,
		},
		{
			name:     "identical text merges",
			a:        "sqlite wal checkpoint keeps main database current",
			b:        "sqlite wal checkpoint keeps main database current",
			wantHigh: true,
		},
		{
			name:     "distinct facts sharing vocabulary do not merge",
			a:        "deploy pipeline fix pinned runner image",
			b:        "deploy pipeline broke after runner image update",
			wantHigh: false,
		},
		{
			name:     "unrelated facts do not merge",
			a:        "sqlite wal checkpoint keeps main database current",
			b:        "kubernetes pod eviction under memory pressure",
			wantHigh: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aTokens, _ := tokenize(tc.a)
			bTokens, _ := tokenize(tc.b)
			sim := jaccardSimilarity(aTokens, bTokens)
			isHigh := sim >= similarityMergeThreshold
			if isHigh != tc.wantHigh {
				t.Fatalf("jaccardSimilarity(%q, %q) = %.3f, want >= %.2f: %v, got: %v",
					tc.a, tc.b, sim, similarityMergeThreshold, tc.wantHigh, isHigh)
			}
		})
	}
}

func TestJaccardSimilarityEmptyInputs(t *testing.T) {
	if got := jaccardSimilarity(nil, nil); got != 0 {
		t.Fatalf("jaccardSimilarity(nil, nil) = %v, want 0", got)
	}
	if got := jaccardSimilarity([]string{"a"}, nil); got != 0 {
		t.Fatalf("jaccardSimilarity(non-empty, nil) = %v, want 0", got)
	}
}
