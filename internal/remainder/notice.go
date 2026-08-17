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
	body, _, truncated := CapWithSpoolRef(spool, principal, result, maxBytes)
	return body, truncated
}

// CapWithSpoolRef is CapWithSpool that also reports the ref it minted.
//
// A second shaping pass needs the ref itself, not just the notice that names
// it: re-cutting the original body means naming the SAME remainder again, and
// recovering that ref by parsing it back out of the notice would make the
// notice's wording a load-bearing interface. The ref is empty when the body
// was not truncated, when there is no spool, or when the store failed.
func CapWithSpoolRef(spool *Spool, principal string, result string, maxBytes int) (body, ref string, truncated bool) {
	if maxBytes <= 0 || len(result) <= maxBytes {
		return result, "", false
	}
	total := len(result)
	if spool != nil {
		ref = spool.Spool(context.Background(), principal, []byte(result))
	}
	return fitTruncation(result, total, maxBytes, ref, ""), ref, true
}

// Fit builds "content + trailer + notice" within maxBytes from an ALREADY
// stored body: it performs no store, reports total as the body's true size,
// and names ref (empty for a plain notice).
//
// The trailer is caller-supplied framing charged inside the same envelope.
// Its whole reason for existing is that a status line appended AFTER a built
// body would push the result past the budget that built it; composed here, the
// envelope bound holds by construction.
func Fit(body string, total, maxBytes int, ref, trailer string) string {
	return fitTruncation(body, total, maxBytes, ref, trailer)
}

// fitTruncation builds content+trailer+notice under maxBytes. Notice length
// depends on the kept digit width, so the cut is refined until the envelope
// fits.
//
// A partial content reference is never emitted: if the budget cannot hold the
// full ref notice, the ref is dropped entirely (INV-AG-10).
func fitTruncation(result string, total, maxBytes int, ref, trailer string) string {
	// Prefer a notice that names the remainder; fall back to a plain notice
	// when the budget cannot hold the full ref (never clip mid-ref).
	effectiveRef := ref
	noticeBudget := len(TruncationNotice(total, total, effectiveRef)) + len(trailer)
	if noticeBudget > maxBytes {
		effectiveRef = ""
		noticeBudget = len(TruncationNotice(total, total, "")) + len(trailer)
	}
	if noticeBudget > maxBytes {
		// Degenerate: even the plain notice does not fit; clip it only (no
		// ref: prefix can appear). A ref notice that fits EXACTLY is kept: at
		// noticeBudget == maxBytes the normal branch below yields bodyBudget =
		// 0 and content = "", and TruncationNotice(0, total, ref) is <=
		// maxBytes because a one-digit kept count is never wider than the
		// reserve computed from total - which already included the trailer.
		notice := trailer + TruncationNotice(0, total, "")
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
	for len(content)+len(trailer)+len(notice) > maxBytes && len(content) > 0 {
		content = trimPartialRune(content[:len(content)-1])
		notice = TruncationNotice(len(content), total, effectiveRef)
	}
	// No second ref-dropping fallback is needed here: kept <= total, so the
	// kept count is never wider than the reserve computed from total - which
	// already included the trailer - and the loop above always converges with
	// the ref intact or with no content left.
	return content + trailer + notice
}

// trimPartialRune returns the longest prefix of s that is valid UTF-8. It
// walks the string forward with utf8.DecodeRuneInString, so a long valid
// prefix followed by an invalid byte costs O(n) instead of the O(n^2) rescans
// of the previous trim-one-byte loop (P-1). A decoded U+FFFD with size 3 is a
// genuine replacement character and does not stop the walk; only RuneError
// with size 1 (an invalid or truncated sequence) does. This matches the O(n)
// invariant TruncateUTF8 establishes in internal/diff/diff.go.
func trimPartialRune(s string) string {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		i += size
	}
	return s[:i]
}
