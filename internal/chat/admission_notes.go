package chat

import (
	"fmt"
	"strings"
)

// maxAdmissionNoteNames bounds how many tool names one operator-visible
// admission note lists. A note's job is to say which tools changed, not to
// replay the whole admitted set; on the context path that set reaches 8
// publications x 64 names (MaxAdmissionPublications x the cli load_tools array
// cap), so replaying it unclamped turns bounded state into an unbounded note.
// Mirrors the cli maxLoadToolsErrorCandidates bound.
const maxAdmissionNoteNames = 20

// maxAdmissionNoteRunes is the hard cap on a rendered admission name list. The
// count bound above cannot stop one pathological name from being arbitrarily
// long, and the note is written to durable storage in full, so the total
// rendered length is clamped too.
const maxAdmissionNoteRunes = 512

// boundedNames renders names for an operator-visible admission note: at most
// max entries joined with ", ", a "… and K more" suffix once entries were cut
// off, and a final rune-count clamp so no input - however many names, however
// long each one - can ever produce an unbounded note. Cutting on a rune
// boundary keeps the result valid UTF-8. chat cannot import internal/cli, so
// this is the local mirror of cli's boundedNameList.
func boundedNames(names []string, max int) string {
	if len(names) == 0 || max < 1 {
		return ""
	}
	shown := names
	if len(names) > max {
		shown = names[:max]
	}
	joined := strings.Join(shown, ", ")
	if len(names) > max {
		joined += fmt.Sprintf("… and %d more", len(names)-max)
	}
	if runes := []rune(joined); len(runes) > maxAdmissionNoteRunes {
		joined = string(runes[:maxAdmissionNoteRunes-1]) + "…"
	}
	return joined
}
