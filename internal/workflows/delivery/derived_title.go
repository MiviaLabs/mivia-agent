package delivery

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
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
// base is truncated (rune-safe - never splits a multi-byte UTF-8 sequence)
// so base+" "+affix never exceeds maxRunes. Both callers ALWAYS use this
// result, never erroring on an overflow: by the time either runs, the
// reused base (an agent's own pr_title, already checked alone by
// sanitizeAgentTitle; or a parent PR's real title) is already known-valid
// on its own. An overflow here is caused entirely by the HOST's own affix
// pushing an already-valid title over the edge - never by anything the
// title's author did wrong, and the author never even sees the affix
// before it's appended. Rejecting that into a repair loop would ask an
// agent to "fix" a title that was already fine, for a reason it has no way
// to anticipate - confusing, and it burns a repair attempt on a purely
// cosmetic overflow instead of making forward progress. A stack-part PR
// created with appendStackPartTitle used to reject this case with a
// PRMetadataError; it now truncates identically to followUpPRContent, one
// mechanism for both, no divergent response paths.
func deriveTitle(baseTitle, affix string, maxRunes int) (title string) {
	base := strings.TrimRight(strings.TrimSpace(baseTitle), " ")
	if base == "" {
		return truncateRunes(affix, maxRunes)
	}
	full := base + " " + affix
	if utf8.RuneCountInString(full) <= maxRunes {
		return full
	}
	room := maxRunes - utf8.RuneCountInString(affix) - 1 // 1 for the separating space
	if room <= 0 {
		return truncateRunes(affix, maxRunes)
	}
	return truncateRunes(base, room) + " " + affix
}

// sanitizeReusedTitle makes a title fetched LIVE from GitHub (parentRef.Title
// in followUpPRContent) safe to reuse as another PR's title base. Unlike
// sanitizeAgentTitle (prmetadata_validate.go), which runs once at PR
// creation and REJECTS a bad agent-authored title via a repairable
// PRMetadataError, a reused title has already been published - a human or
// another tool may have hand-edited it on GitHub after creation, bypassing
// sanitizeAgentTitle entirely, and followUpPRContent has no repair loop to
// reject into. So this strips rather than rejects: control characters
// (including one a hand-edit could introduce that sanitizeAgentTitle would
// have caught at creation time) are dropped, embedded newlines/tabs fold to
// spaces via foldToSingleLine, and any secret-shaped substring is redacted -
// the same three transforms sanitizeAgentTitle applies, minus its
// reject-on-control-character step.
func sanitizeReusedTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	for _, r := range title {
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || r == '\u2028' || r == '\u2029' {
			continue
		}
		b.WriteRune(r)
	}
	return redact.Text(foldToSingleLine(b.String()))
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
