package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// entryID is a content-addressed id: identical scope+org+title+content
// produce the same id, which makes an identical re-save idempotent within
// one (scope, org) namespace. The org identity is part of the hash, so two
// org IDs sharing one database file cannot collide on the same content: an
// identical entry saved under org B must not resolve to org A's row.
func entryID(scope Scope, org, title, rendered string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + org + "\x00" + title + "\x00" + rendered))
	return hex.EncodeToString(sum[:16])
}

// escapeLike escapes LIKE wildcards so a user query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, tag := range strings.Split(s, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// rankMatch scores a row for a parsed query: 0 full query equals title, 1
// full query (phrase) in title, 2 in summary, 3 in content, 4 all tokens and
// phrases in title, 5 in summary, 6 in content, 7 spread across fields, -1 no
// match. The ordering mirrors the SQLite rank CASE so both backends rank
// identically.
func rankMatch(title, summary, body, lowerText string, p parsedQuery) int {
	if strings.EqualFold(title, lowerText) {
		return 0
	}
	lowerTitle := strings.ToLower(title)
	if strings.Contains(lowerTitle, lowerText) {
		return 1
	}
	lowerSummary := strings.ToLower(summary)
	if strings.Contains(lowerSummary, lowerText) {
		return 2
	}
	lowerBody := strings.ToLower(body)
	if strings.Contains(lowerBody, lowerText) {
		return 3
	}
	if p.zeroToken {
		return -1
	}
	if matchParts(lowerTitle, p) {
		return 4
	}
	if matchParts(lowerSummary, p) {
		return 5
	}
	if matchParts(lowerBody, p) {
		return 6
	}
	if matchParts(lowerTitle+"\n"+lowerSummary+"\n"+lowerBody, p) {
		return 7
	}
	return -1
}

// matchParts reports whether every token appears as a substring and every
// phrase as a contiguous substring in lowerText.
func matchParts(lowerText string, p parsedQuery) bool {
	for _, tok := range p.tokens {
		if !strings.Contains(lowerText, tok) {
			return false
		}
	}
	for _, phrase := range p.phrases {
		if !strings.Contains(lowerText, phrase) {
			return false
		}
	}
	return true
}

// mergeRanked merges project and org results, re-ranks, and takes the top
// limit. Both inputs are already filtered match sets; a row whose match cannot
// be localized to its title or snippet re-ranks to 7 (tokens spread across
// fields is the lowest rank), never above a title or summary match.
func mergeRanked(proj, org []Result, p parsedQuery, text string, limit int) []Result {
	lowerText := strings.ToLower(text)
	all := make([]Result, 0, len(proj)+len(org))
	all = append(all, proj...)
	all = append(all, org...)
	type scored struct {
		r    Result
		rank int
	}
	scoredAll := make([]scored, len(all))
	for i, r := range all {
		rank := rankMatch(r.Title, r.Snippet, "", lowerText, p)
		if rank < 0 {
			rank = 7 // matched somewhere in content; tokens spread is the lowest rank
		}
		scoredAll[i] = scored{r, rank}
	}
	sort.SliceStable(scoredAll, func(i, j int) bool {
		if scoredAll[i].rank != scoredAll[j].rank {
			return scoredAll[i].rank < scoredAll[j].rank
		}
		if scoredAll[i].r.Created != scoredAll[j].r.Created {
			return scoredAll[i].r.Created > scoredAll[j].r.Created
		}
		if scoredAll[i].r.Title != scoredAll[j].r.Title {
			return scoredAll[i].r.Title < scoredAll[j].r.Title
		}
		return scoredAll[i].r.ID < scoredAll[j].r.ID
	})
	if len(scoredAll) > limit {
		scoredAll = scoredAll[:limit]
	}
	out := make([]Result, len(scoredAll))
	for i, m := range scoredAll {
		out[i] = m.r
	}
	return out
}
