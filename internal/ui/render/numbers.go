package render

import (
	"fmt"
	"strconv"
)

// CompactTokens renders token counts below 1000 exactly and larger counts
// with one decimal in thousands (for example, 1284 becomes 1.3k).
func CompactTokens(n int64) string {
	if n < 1000 && n > -1000 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// GroupThousands renders an integer with a comma every three digits.
func GroupThousands(n int) string {
	var u uint64
	neg := n < 0
	if neg {
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
