package remainder

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// TruncationNotice formats the model-visible truncation trailer.
//
// When ref is non-empty the notice names the remainder and directs the model
// to read_output. When ref is empty (store failed, or no spool) the notice
// still reports kept/total honestly and invents no reference.
func TruncationNotice(kept, total int, ref string) string {
	if ref != "" {
		return fmt.Sprintf("\n... truncated: kept %d of %d bytes (remainder: %s, use read_output)", kept, total, ref)
	}
	return fmt.Sprintf("\n... truncated: kept %d of %d bytes", kept, total)
}

// CapWithSpool shortens result so the whole return value fits in maxBytes,
// spools the original body for principal, and appends an honest truncation
// notice. When no truncation is needed the original body is returned unchanged.
//
// maxBytes <= 0 means uncapped (no truncation). A nil spool (or failed store)
// yields a notice without a ref - the call still succeeds (INV-CE-07-C).
func CapWithSpool(spool *Spool, principal string, result string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(result) <= maxBytes {
		return result, false
	}
	total := len(result)
	ref := ""
	if spool != nil {
		ref = spool.Spool(context.Background(), principal, []byte(result))
	}
	return fitTruncation(result, total, maxBytes, ref), true
}

// fitTruncation builds content+notice under maxBytes. Notice length depends on
// the kept digit width, so the cut is refined until the envelope fits.
//
// A partial content reference is never emitted: if the budget cannot hold the
// full ref notice, the ref is dropped entirely (INV-AG-10).
func fitTruncation(result string, total, maxBytes int, ref string) string {
	// Prefer a notice that names the remainder; fall back to a plain notice
	// when the budget cannot hold the full ref (never clip mid-ref).
	effectiveRef := ref
	noticeBudget := len(TruncationNotice(total, total, effectiveRef))
	if noticeBudget > maxBytes {
		effectiveRef = ""
		noticeBudget = len(TruncationNotice(total, total, ""))
	}
	if noticeBudget >= maxBytes {
		// Degenerate: even a plain notice does not fit; clip the plain notice
		// only (no ref: prefix can appear).
		notice := TruncationNotice(0, total, "")
		if len(notice) > maxBytes {
			return trimPartialRune(notice[:maxBytes])
		}
		return notice
	}
	bodyBudget := maxBytes - noticeBudget
	if bodyBudget > len(result) {
		bodyBudget = len(result)
	}
	content := trimPartialRune(result[:bodyBudget])
	notice := TruncationNotice(len(content), total, effectiveRef)
	for len(content)+len(notice) > maxBytes && len(content) > 0 {
		content = trimPartialRune(content[:len(content)-1])
		notice = TruncationNotice(len(content), total, effectiveRef)
	}
	// No second ref-dropping fallback is needed here: kept <= total, so the
	// kept count is never wider than the reserve computed from total, and the
	// loop above always converges with the ref intact or with no content left.
	return content + notice
}

func trimPartialRune(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
