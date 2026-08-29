package sel

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// HighlightLines wraps the cells of lines between from..to (inclusive,
// reading order normalized) in reverse video (SGR 7 / SGR 27), the
// same live-drag feedback a terminal's own selection gives. It operates
// on already-rendered styled rows: Cut and StringWidth are display-
// width aware, so wide/CJK cells and ANSI runs stay aligned. Rows
// outside [from.Row, to.Row] are returned untouched; out-of-range rows
// clamp so a caller may pass a selection taller than its line slice.
func HighlightLines(lines []string, from, to Cell) []string {
	if to.Row < from.Row || (to.Row == from.Row && to.Col < from.Col) {
		from, to = to, from
	}
	out := make([]string, len(lines))
	copy(out, lines)
	for row := from.Row; row <= to.Row && row < len(out); row++ {
		if row < 0 {
			continue
		}
		line := out[row]
		w := ansi.StringWidth(line)
		left, right := 0, w
		if row == from.Row {
			left = clampInt(from.Col, 0, w)
		}
		if row == to.Row {
			right = clampInt(to.Col+1, 0, w)
		}
		if right <= left {
			continue
		}
		prefix := ansi.Cut(line, 0, left)
		middle := ansi.Cut(line, left, right)
		suffix := ansi.Cut(line, right, w)
		out[row] = prefix + "\x1b[7m" + middle + "\x1b[27m" + suffix
	}
	return out
}

// StreamSelect extracts the plain-text stream selection between two
// region-local cells from rows, normalizing so the earlier point in
// reading order comes first. This is a stream selection (terminal
// click-drag semantics), not a block selection: the anchor row runs
// from its column to the row's end, inner rows are taken whole, and
// the end row runs from its start to the focus column inclusive.
// Out-of-range rows clamp; styles are stripped from the result.
func StreamSelect(rows []string, from, to Cell) string {
	if to.Row < from.Row || (to.Row == from.Row && to.Col < from.Col) {
		from, to = to, from
	}
	if from.Row < 0 {
		from.Row = 0
	}
	if to.Row >= len(rows) {
		to.Row = len(rows) - 1
	}
	if to.Row < from.Row {
		return ""
	}
	var sb strings.Builder
	for row := from.Row; row <= to.Row; row++ {
		line := rows[row]
		w := ansi.StringWidth(line)
		left, right := 0, w
		if row == from.Row {
			left = clampInt(from.Col, 0, w)
		}
		if row == to.Row {
			right = clampInt(to.Col+1, 0, w)
		}
		// right >= left always: from<=to per the ordering above, and
		// clampInt is monotonic, so this row's right bound (w, or
		// to.Col+1 clamped) never falls below its left bound (0, or
		// from.Col clamped).
		sb.WriteString(ansi.Strip(ansi.Cut(line, left, right)))
		if row != to.Row {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
