package memory

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query parsing caps. Tokens are truncated at maxTokenLen runes and the token
// list at maxQueryTokens entries: oversized query input is bounded, never an
// error, so untrusted query text cannot grow memory or work unboundedly.
const (
	maxTokenLen    = 64
	maxQueryTokens = 64
)

// stopwords carry no signal for a keyword search and are dropped before
// matching, so 'the cache' is equivalent to 'cache' on both backends. The
// list is the plan's small set; FTS5 does not know it, so the FTS MATCH
// builder drops the same words to keep its candidate set aligned.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "of": {}, "to": {}, "for": {}, "on": {},
	"in": {}, "at": {}, "with": {}, "by": {}, "and": {}, "or": {}, "from": {},
	"as": {}, "is": {}, "are": {}, "was": {}, "be": {}, "it": {}, "its": {}, "that": {},
}

// isTokenRune reports whether r can be part of a query token: a Unicode
// letter or digit, matching FTS5 unicode61's default token characters.
// Everything else - whitespace, punctuation, underscore, quotes - is a
// separator, so a token can never contain a rune FTS5 would split, keeping
// the FTS and LIKE candidate sets aligned.
func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// tokenize splits q into lowercase, deduplicated, stopword-free tokens. Token
// characters are Unicode letters and digits; every other rune (whitespace,
// punctuation, underscore, quotes) is a separator. Tokens are capped at
// maxTokenLen runes and the list at maxQueryTokens entries (truncation, never
// an error). zeroToken is true when no tokens remain, so the caller can fall
// back to today's whole-phrase substring behavior.
func tokenize(q string) (tokens []string, zeroToken bool) {
	seen := make(map[string]struct{}, 8)
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		tok := strings.ToLower(string(cur))
		cur = cur[:0]
		if len(tokens) >= maxQueryTokens {
			return
		}
		if _, ok := stopwords[tok]; ok {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		tokens = append(tokens, tok)
	}
	for _, r := range q {
		if isTokenRune(r) {
			if len(cur) < maxTokenLen {
				cur = append(cur, r)
			}
			continue
		}
		flush()
	}
	flush()
	return tokens, len(tokens) == 0
}

// allQuotesDoubled reports whether every double quote in q is part of a
// doubled pair (""), i.e. there is no un-doubled quote that could delimit a
// phrase. A query like 'say ""hi""' then means one implicit phrase spanning
// the whole query whose escaped quotes un-double to 'say "hi"'. A query with
// NO quotes at all has no implicit phrase: it is false, so an unquoted
// multi-word query is matched by its tokens (order-independent), never as a
// single contiguous phrase.
func allQuotesDoubled(q string) bool {
	sawQuote := false
	for i := 0; i < len(q); i++ {
		if q[i] != '"' {
			continue
		}
		sawQuote = true
		if i+1 >= len(q) || q[i+1] != '"' {
			return false
		}
		i++
	}
	return sawQuote
}

// extractPhrases returns the lowercased text of every balanced double-quoted
// phrase in q, with doubled inner quotes un-escaped to a single quote. A
// phrase must appear in the stored text contiguously and in order; unquoted
// words are matched order-independently. Unbalanced quotes are not a phrase:
// the tokenizer treats quotes as separators, so a dangling quote degrades the
// query to token matching rather than a partial phrase. When every quote is
// part of a doubled pair (for example 'say ""hi""'), the whole query is one
// implicit phrase and the pairs un-double to literal quotes.
func extractPhrases(q string) []string {
	if allQuotesDoubled(q) {
		if content := strings.TrimSpace(strings.ReplaceAll(q, `""`, `"`)); content != "" && strings.Trim(content, `"`) != "" {
			return []string{strings.ToLower(content)}
		}
		return nil
	}
	var phrases []string
	for i := 0; i < len(q); {
		if q[i] != '"' {
			i++
			continue
		}
		j := i + 1
		var b strings.Builder
		closed := false
		for j < len(q) {
			if q[j] == '"' {
				if j+1 < len(q) && q[j+1] == '"' {
					b.WriteByte('"')
					j += 2
					continue
				}
				closed = true
				j++
				break
			}
			b.WriteByte(q[j])
			j++
		}
		if !closed {
			break
		}
		if b.Len() > 0 {
			phrases = append(phrases, strings.ToLower(b.String()))
		}
		i = j
	}
	return phrases
}

// parsedQuery is the decomposed search query shared by both backends.
type parsedQuery struct {
	// tokens must each appear as a substring in lower(title|summary|content).
	tokens []string
	// phrases must each appear as a contiguous substring in the same fields.
	phrases []string
	// zeroToken is true when no tokens and no phrases remain: the full query
	// text is matched as one contiguous phrase (today's behavior).
	zeroToken bool
}

// parseQuery decomposes a trimmed query into tokens and phrases.
func parseQuery(q string) parsedQuery {
	tokens, _ := tokenize(q)
	phrases := extractPhrases(q)
	return parsedQuery{
		tokens:    tokens,
		phrases:   phrases,
		zeroToken: len(tokens) == 0 && len(phrases) == 0,
	}
}

// dropTokens returns tokens with the n longest entries removed; length ties
// drop the leftmost. The survivors keep their original order. It is a pure
// function, so both backends relax a zero-hit query identically.
func dropTokens(tokens []string, n int) []string {
	if n <= 0 || n >= len(tokens) {
		return nil
	}
	order := make([]int, len(tokens))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		li, lj := utf8.RuneCountInString(tokens[order[i]]), utf8.RuneCountInString(tokens[order[j]])
		if li != lj {
			return li > lj
		}
		return order[i] < order[j]
	})
	drop := make(map[int]struct{}, n)
	for _, idx := range order[:n] {
		drop[idx] = struct{}{}
	}
	out := make([]string, 0, len(tokens)-n)
	for i, tok := range tokens {
		if _, ok := drop[i]; !ok {
			out = append(out, tok)
		}
	}
	return out
}

// relaxSearch runs search with the full token list and, when a multi-token
// query returns zero results, retries dropping the longest tokens - at most
// two retries (retry k drops k tokens). Single-token and phrase-only queries
// never relax. Both backends share this wrapper, so zero-hit relaxation is
// deterministic and identical. search always receives a non-empty token list.
func relaxSearch(tokens []string, search func([]string) ([]Result, error)) ([]Result, error) {
	res, err := search(tokens)
	if err != nil || len(res) > 0 || len(tokens) < 2 {
		return res, err
	}
	for retry := 1; retry <= 2; retry++ {
		relaxed := dropTokens(tokens, retry)
		if len(relaxed) == 0 {
			break
		}
		r, err := search(relaxed)
		if err != nil {
			return nil, err
		}
		if len(r) > 0 {
			return r, nil
		}
	}
	return res, nil
}
