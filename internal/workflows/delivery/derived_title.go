package delivery

import (
	"strings"
	"unicode/utf8"
)

// deriveTitle is the ONE title+affix+length primitive both stacking
// (appendStackPartTitle) and the deferred/split follow-up (followUpPRContent)
// build on: baseTitle stays the first, human-authored words, and affix is a
// short bracketed tag identifying the relationship and its merge-order
// dependency ("[stack k/N]", "[split k/N, base: #142]") - one consistent
// convention for the same "this PR derives from another PR" relationship,
// never two differently-shaped sentences. Single-line by construction: a PR
// title is a one-line field everywhere it is displayed.
//
// It also owns the ONE length-bounding calculation for a derived title: the
// base is truncated so base+" "+affix never exceeds maxRunes. fits reports
// whether the UNTRUNCATED result would have fit, and fullRunes is that
// untruncated result's own rune count - the exact number an overflow caller
// should report, never a value it recomputes itself. A caller that
// re-measured independently (e.g. from the raw, untrimmed baseTitle) could
// diverge from what THIS function actually measured whenever baseTitle
// carries leading/trailing whitespace deriveTitle trims away, handing a
// repair agent a misleading count. Centralizing the truncation math here
// means both callers measure identically; only the RESPONSE to an overflow
// differs, because they have different recovery paths available:
//   - appendStackPartTitle runs inside the repairable validatePRMetadata
//     path and can return a PRMetadataError to route to a repair step, so it
//     rejects an overflow rather than silently shortening the agent's title.
//   - followUpPRContent (EnsureFollowUpPublished) has no repair loop; an
//     overflow there would fail pr.Create outright and leave the deferred
//     branch permanently unpublished, so it always uses the truncated
//     result instead of erroring.
func deriveTitle(baseTitle, affix string, maxRunes int) (title string, fits bool, fullRunes int) {
	base := strings.TrimRight(strings.TrimSpace(baseTitle), " ")
	if base == "" {
		affixRunes := utf8.RuneCountInString(affix)
		return truncateRunes(affix, maxRunes), affixRunes <= maxRunes, affixRunes
	}
	full := base + " " + affix
	fullRunes = utf8.RuneCountInString(full)
	if fullRunes <= maxRunes {
		return full, true, fullRunes
	}
	room := maxRunes - utf8.RuneCountInString(affix) - 1 // 1 for the separating space
	if room <= 0 {
		return truncateRunes(affix, maxRunes), false, fullRunes
	}
	return truncateRunes(base, room) + " " + affix, false, fullRunes
}

// prLinkMarkdown renders ref as a markdown link ("[#142](url)") so every PR
// mentioned in a derived PR's body is a clickable reference, never a bare
// "#142" a reviewer has to resolve by hand. Falls back to the bare "#id" (or
// "" when even that is unknown) when URL is missing, which only happens for
// a caller-constructed PRRef in a defensive fallback path.
func prLinkMarkdown(ref PRRef) string {
	if ref.RemoteID == "" {
		return ""
	}
	if ref.URL == "" {
		return "#" + ref.RemoteID
	}
	return "[#" + ref.RemoteID + "](" + ref.URL + ")"
}
