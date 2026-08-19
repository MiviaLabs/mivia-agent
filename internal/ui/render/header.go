package render

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// HeaderSpec is the content of a block header row, in four columns.
// wireframes-panes.md section 2: the collapse marker at column 1, the
// label at column 3, the detail after it, then a right-aligned meta and
// a right-aligned state.
type HeaderSpec struct {
	// Marker is the collapse glyph, "v", ">" or " ".
	Marker string
	Label  string
	Detail string
	Meta   string
	State  string
	// StateRole colours the state word. The word is always rendered, so
	// meaning never depends on colour alone.
	StateRole theme.Role
}

// minHeaderGap keeps at least this much space between the detail and the
// right-aligned columns, so they never read as one run of text.
const minHeaderGap = 2

// Header renders a block header, right-aligning meta and state against
// width. When the columns cannot all fit, the DETAIL is clipped and
// marked - never the state, which carries meaning, and never the label,
// which identifies the block.
//
// Contract, relied on by Block.Height: for width > 0 the result is AT
// MOST width display columns and contains no newline; it is exactly
// width whenever a meta or a state is present to right-align. A header
// that overflowed would wrap, and the live window would then budget one
// row for content that draws two.
//
// Pure: input in, string out, no I/O and no package state.
func Header(t theme.Theme, tier theme.Tier, width int, spec HeaderSpec) string {
	out := headerRow(t, tier, width, spec)
	if width <= 0 {
		return out
	}
	return clampWidth(out, width)
}

// clampWidth is the last word on the width contract. Truncation is not
// exact for every grapheme cluster - a variation selector can widen the
// rune it follows after the cut - so the result is measured and cut again
// until it fits. Normal input returns on the first pass.
func clampWidth(s string, width int) string {
	for limit := width; limit > 1; limit-- {
		if out := ansi.Truncate(s, limit, ""); ansi.StringWidth(out) <= width {
			return out
		}
	}
	// Last resort, and the only path for a width of one. A single-column
	// truncation cannot exceed a width of one or more, and one or more is
	// the only width that reaches here.
	return ansi.Truncate(s, 1, "")
}

func headerRow(t theme.Theme, tier theme.Tier, width int, spec HeaderSpec) string {
	spec = sanitizeSpec(spec)
	lead := spec.Marker + " " + spec.Label
	right := spec.Meta
	if spec.State != "" {
		if right != "" {
			right += "  "
		}
		right += spec.State
	}

	// A width of zero means "unknown": fall back to single spaces rather
	// than inventing a column layout for a terminal we have not measured.
	if width <= 0 {
		return styleHeader(t, tier, spec, lead, spec.Detail, "  ", right)
	}

	// Degenerate: the right columns alone do not fit. The state carries
	// the meaning, so it survives alone and the meta is dropped. Keeping
	// the meta instead would leave a header reading "1234ms" with no word
	// saying whether the call succeeded.
	if ansi.StringWidth(right) >= width {
		word, role := spec.State, spec.StateRole
		if role == "" {
			role = theme.RoleFGMuted
		}
		if word == "" {
			word, role = spec.Meta, theme.RoleFGSubtle
		}
		word = ansi.Truncate(word, width, "")
		// Still right-aligned: the survivor keeps the column it had.
		return pad(width-ansi.StringWidth(word)) + Role(t, tier, role).Render(word)
	}

	lead, detail, gap := fit(lead, spec.Detail, right, width)
	return styleHeader(t, tier, spec, lead, detail, gap, right)
}

// fit solves the column layout at a known width. It returns the lead
// (marker and label), the detail, and the gap that right-aligns the meta
// and state. The three plus the right columns always total width.
func fit(lead, detail, right string, width int) (string, string, string) {
	rightW := ansi.StringWidth(right)
	avail := width - rightW
	if right != "" {
		avail -= minHeaderGap
	}
	if avail <= 0 {
		return "", "", pad(width - rightW)
	}

	if ansi.StringWidth(lead) > avail {
		lead = ansi.Truncate(lead, avail, "")
		detail = ""
	} else {
		detail = clipDetail(detail, avail-ansi.StringWidth(lead))
	}

	// Measured whole, not as a sum of parts: see the backstop in Header.
	left := lead
	if detail != "" {
		left += " " + detail
	}
	return lead, detail, pad(width - ansi.StringWidth(left) - rightW)
}

// pad is a run of n spaces, never a negative Repeat. Truncation is not
// exact for every grapheme cluster, so a caller can compute a negative
// remainder from a correct-looking subtraction.
func pad(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// clipDetail shortens the detail to the room left beside the lead,
// marking the cut. room includes the separating space.
func clipDetail(detail string, room int) string {
	if detail == "" {
		return ""
	}
	room-- // the space between the lead and the detail
	if room < 1 {
		return ""
	}
	if ansi.StringWidth(detail) <= room {
		return detail
	}
	markW := ansi.StringWidth(uikitconfig.ClipMarker)
	if room <= markW {
		return ""
	}
	return ansi.Truncate(detail, room-markW, "") + uikitconfig.ClipMarker
}

// headerSanitizer folds every C0 control and DEL to a space. A tab or a
// newline breaks the single-row contract; a bare ESC is worse, because
// the width counter then reads the text after it as an escape sequence
// and stops counting real columns. Header fields are CONTENT, never
// markup: the styling is applied here, not carried in.
func headerSanitizer(r rune) rune {
	if r < 0x20 || r == 0x7f {
		return ' '
	}
	// Format characters are dropped, not spaced. They are invisible, they
	// join with the next rune into one grapheme cluster - which makes the
	// row's width depend on where the style escapes land - and a bidi
	// override inside a file path can make it read as a different path.
	if unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}

// sanitizeField also repairs invalid UTF-8, which tool output does carry.
// Invalid bytes have no stable display width: they combine differently
// depending on what is concatenated next, so width(a)+width(b) stops
// equalling width(a+b) and every column calculation drifts. Replacing
// them makes the width additive again, and U+FFFD is what a terminal
// would have drawn anyway.
func sanitizeField(s string) string {
	return strings.Map(headerSanitizer, strings.ToValidUTF8(s, "�"))
}

func sanitizeSpec(spec HeaderSpec) HeaderSpec {
	spec.Marker = sanitizeField(spec.Marker)
	spec.Label = sanitizeField(spec.Label)
	spec.Detail = sanitizeField(spec.Detail)
	spec.Meta = sanitizeField(spec.Meta)
	spec.State = sanitizeField(spec.State)
	return spec
}

func styleHeader(t theme.Theme, tier theme.Tier, spec HeaderSpec, lead, detail, gap, right string) string {
	// The marker and label are dim; the detail is normal weight; meta is
	// subtle; the state carries its own role.
	var out string
	if lead != "" {
		out = Role(t, tier, theme.RoleFGMuted).Render(lead)
	}
	if detail != "" {
		out += " " + Role(t, tier, theme.RoleFG).Render(detail)
	}
	if right == "" {
		return out
	}
	out += gap
	if spec.Meta != "" {
		out += Role(t, tier, theme.RoleFGSubtle).Render(spec.Meta)
		if spec.State != "" {
			out += "  "
		}
	}
	if spec.State != "" {
		role := spec.StateRole
		if role == "" {
			role = theme.RoleFGMuted
		}
		out += Role(t, tier, role).Render(spec.State)
	}
	return out
}
