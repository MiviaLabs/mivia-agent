package memory

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTokenizeSplitsAndLowercases(t *testing.T) {
	cases := []struct {
		query string
		want  []string
		zero  bool
	}{
		{"DeepSeek 400", []string{"deepseek", "400"}, false},
		{"v4-flash", []string{"v4", "flash"}, false},
		{"CACHE", []string{"cache"}, false},
		{"deep_learning", []string{"deep", "learning"}, false}, // underscore is a separator (unicode61 parity)
		{"_done", []string{"done"}, false},
		{"v2.0", []string{"v2", "0"}, false},
		{"naïve plan", []string{"naïve", "plan"}, false},                       // unicode letters are token chars
		{"cache cache invalidation", []string{"cache", "invalidation"}, false}, // dedupe keeps first order
		{"the of", nil, true},                                                  // stopword-only -> zero tokens with fallback flag
		{"A An THE and or", nil, true},
		{"", nil, true},
		{"   \t  ", nil, true},
		{"-", nil, true},
		{`"exact phrase"`, []string{"exact", "phrase"}, false}, // quotes are separators too
		{"a  b   c", []string{"b", "c"}, false},                // stopword dropped, runs of separators
		{"  cache  ", []string{"cache"}, false},                // leading/trailing separators
		{"\x01cache", []string{"cache"}, false},                // control chars are separators
		{`"exact phrase`, []string{"exact", "phrase"}, false},  // unbalanced quote degrades to tokens
	}
	for _, tc := range cases {
		got, zero := tokenize(tc.query)
		if zero != tc.zero {
			t.Errorf("tokenize(%q) zeroToken = %v, want %v", tc.query, zero, tc.zero)
		}
		if !equalStrings(got, tc.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestTokenizeOversizedInputBounded(t *testing.T) {
	// A 65-rune token is truncated to maxTokenLen, never an error.
	long := strings.Repeat("a", 65)
	got, zero := tokenize(long)
	if zero || len(got) != 1 || len(got[0]) != maxTokenLen {
		t.Fatalf("tokenize(65-rune token) = %v (zero=%v), want one %d-rune token", got, zero, maxTokenLen)
	}
	// A 65-part query of DISTINCT tokens is capped at maxQueryTokens entries:
	// the fixture must use unique tokens, because tokenize dedupes the token
	// list (a repeated cycle of 26 tokens would never reach the 64 cap).
	parts := make([]string, 65)
	for i := range parts {
		parts[i] = "w" + strconv.Itoa(i)
	}
	q := strings.Join(parts, " ")
	got, zero = tokenize(q)
	if zero || len(got) != maxQueryTokens {
		t.Fatalf("tokenize(65 parts) = %d tokens (zero=%v), want cap %d", len(got), zero, maxQueryTokens)
	}
}

func TestExtractPhrases(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{`"exact phrase"`, []string{"exact phrase"}},
		{`"exact phrase" more`, []string{"exact phrase"}},
		{`say ""hi""`, []string{`say "hi"`}}, // doubled inner quotes un-escape to one
		{`"HTTP 400" and "v4 flash"`, []string{"http 400", "v4 flash"}},
		{`"exact phrase`, nil}, // unbalanced: not a phrase
		{`""`, nil},            // empty phrase skipped
		{`plain words`, nil},
	}
	for _, tc := range cases {
		got := extractPhrases(tc.query)
		if !equalStrings(got, tc.want) {
			t.Errorf("extractPhrases(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestParseQueryZeroTokenSemantics(t *testing.T) {
	// zeroToken is true only when there are no tokens AND no phrases: the full
	// query is matched as one contiguous phrase (today's '-' behavior).
	if p := parseQuery("DeepSeek 400"); p.zeroToken || !equalStrings(p.tokens, []string{"deepseek", "400"}) {
		t.Errorf("parseQuery(multi-word) = %+v", p)
	}
	if p := parseQuery("-"); !p.zeroToken {
		t.Errorf("parseQuery(-) must be zeroToken, got %+v", p)
	}
	if p := parseQuery("the of"); !p.zeroToken {
		t.Errorf("parseQuery(stopword-only) must be zeroToken, got %+v", p)
	}
	if p := parseQuery(`"the of"`); p.zeroToken || !equalStrings(p.phrases, []string{"the of"}) {
		t.Errorf("parseQuery(quoted stopwords) must carry a phrase, got %+v", p)
	}
}

func TestDropTokensDropsLongestLeftmost(t *testing.T) {
	tokens := []string{"ab", "cd", "efgh", "i"}
	got := dropTokens(tokens, 1)
	if !equalStrings(got, []string{"ab", "cd", "i"}) {
		t.Errorf("dropTokens(1) = %v, want [ab cd i] (longest efgh dropped)", got)
	}
	got = dropTokens(tokens, 2)
	if !equalStrings(got, []string{"cd", "i"}) {
		t.Errorf("dropTokens(2) = %v, want [cd i] (length ties drop leftmost)", got)
	}
	got = dropTokens([]string{"deepseek", "400", "bogus"}, 1)
	if !equalStrings(got, []string{"400", "bogus"}) {
		t.Errorf("dropTokens(longest) = %v, want [400 bogus]", got)
	}
	// Survivors keep their original order.
	got = dropTokens([]string{"aaa", "b", "cc"}, 2)
	if !equalStrings(got, []string{"b"}) {
		t.Errorf("dropTokens(2) = %v, want [b]", got)
	}
	if dropTokens(tokens, 0) != nil || dropTokens(tokens, 4) != nil {
		t.Error("dropTokens must return nil for n <= 0 or n >= len")
	}
}

func TestRelaxSearch(t *testing.T) {
	found := func(tokens []string) ([]Result, error) {
		out := make([]Result, 0, len(tokens))
		for _, tok := range tokens {
			if tok == "400" {
				out = append(out, Result{ID: "hit", Title: "hit"})
			}
		}
		return out, nil
	}
	// One retry finds the hit after the longest token is dropped.
	res, err := relaxSearch([]string{"deepseek", "400", "bogus"}, found)
	if err != nil || len(res) != 1 {
		t.Fatalf("relaxSearch = %v, %v; want the relaxed hit", res, err)
	}
	// Two retries: the first retry still misses, the second drops two tokens.
	twoStep := func(tokens []string) ([]Result, error) {
		if len(tokens) == 1 && tokens[0] == "400" {
			return []Result{{ID: "hit2"}}, nil
		}
		return nil, nil
	}
	res, err = relaxSearch([]string{"deepseek", "400", "bogus"}, twoStep)
	if err != nil || len(res) != 1 {
		t.Fatalf("relaxSearch two-step = %v, %v; want the second-retry hit", res, err)
	}
	// All tokens dropped still misses: empty result, no loop.
	res, err = relaxSearch([]string{"deepseek", "400", "bogus"}, func([]string) ([]Result, error) { return nil, nil })
	if err != nil || len(res) != 0 {
		t.Fatalf("relaxSearch all-missing = %v, %v; want empty", res, err)
	}
	// Single-token queries never relax: the search is run once only.
	calls := 0
	_, _ = relaxSearch([]string{"cache"}, func(tokens []string) ([]Result, error) {
		calls++
		if len(tokens) != 1 || tokens[0] != "cache" {
			t.Errorf("single-token relaxSearch called with %v", tokens)
		}
		return nil, nil
	})
	if calls != 1 {
		t.Errorf("single-token relaxSearch ran %d searches, want 1", calls)
	}
	// A search error propagates without relaxing.
	boom := errors.New("boom")
	_, err = relaxSearch([]string{"a", "b"}, func([]string) ([]Result, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("relaxSearch error = %v, want %v", err, boom)
	}
}
