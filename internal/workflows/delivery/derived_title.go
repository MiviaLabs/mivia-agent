package delivery

import "strings"

// derivedPRTitle builds a title for a PR the host derives from another PR's
// own delivery (a stack part, or a deferred split), rather than generating
// one from scratch: baseTitle stays the first, human-authored words, and
// affix is a short bracketed tag identifying the relationship and its
// merge-order dependency. Both stacking (appendStackPartTitle) and the
// deferred/split follow-up (EnsureFollowUpPublished) use this so a reviewer
// sees one consistent convention regardless of which mechanism produced the
// PR - never two differently-shaped sentences for the same "this depends on
// another PR" relationship. Single-line by construction: unlike the old
// "\n\n" trailer, a PR title is a one-line field everywhere it is displayed.
func derivedPRTitle(baseTitle, affix string) string {
	base := strings.TrimRight(strings.TrimSpace(baseTitle), " ")
	if base == "" {
		return affix
	}
	return base + " " + affix
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
