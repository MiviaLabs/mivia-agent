package render

import "strconv"

// GroupThousands renders an integer with a comma every three digits,
// the grouping style docs/design/wireframes-panes.md section 4 already
// uses for token counts ("1,284 in"). The sign of a negative value is
// kept: "-1,284".
//
// It deliberately does not compact to "1.3k": a transcript number a
// user may cite or grep must equal the fact, not an approximation of
// it (see transcript-polish.md R6 for the same decision).
//
// Pure: input in, string out, no I/O and no package state.
func GroupThousands(n int) string {
	var u uint64
	neg := n < 0
	if neg {
		// -(n+1)+1 never overflows; -n would on MinInt.
		u = uint64(-(n + 1)) + 1
	} else {
		u = uint64(n)
	}
	digits := strconv.FormatUint(u, 10)
	out := make([]byte, 0, len(digits)+len(digits)/3+1)
	if neg {
		out = append(out, '-')
	}
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(d))
	}
	return string(out)
}
