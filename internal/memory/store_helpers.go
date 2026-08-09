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

// entryID is a content-addressed id: identical scope+title+content produce
// the same id, which makes an identical re-save idempotent.
func entryID(scope Scope, title, rendered string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + title + "\x00" + rendered))
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

// rankMatch scores a row for a query: 0 exact title, 1 title contains,
// 2 summary contains, 3 body contains, -1 no match. The ordering mirrors the
// SQLite searchSQL CASE so both backends rank identically.
func rankMatch(title, summary, body, lowerText string) int {
	if strings.EqualFold(title, lowerText) {
		return 0
	}
	lowerTitle := strings.ToLower(title)
	if strings.Contains(lowerTitle, lowerText) {
		return 1
	}
	if strings.Contains(strings.ToLower(summary), lowerText) {
		return 2
	}
	if strings.Contains(strings.ToLower(body), lowerText) {
		return 3
	}
	return -1
}

// mergeRanked merges project and org results, re-ranks, and takes the top
// limit. Both inputs are already filtered match sets; a row that matched only
// in body content re-ranks to 3 (its SQLite rank), never above.
func mergeRanked(proj, org []Result, text string, limit int) []Result {
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
		rank := rankMatch(r.Title, r.Snippet, "", lowerText)
		if rank < 0 {
			rank = 3 // matched somewhere; body content is the lowest rank
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
		return scoredAll[i].r.Title < scoredAll[j].r.Title
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
