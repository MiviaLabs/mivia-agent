package render

import "fmt"

// FormatElapsed renders a duration in milliseconds with the one
// duration grammar every surface shares (transcript-polish.md R5;
// docs/design/wireframes-panes.md section 4):
//
//   - below 1s:    "250ms"
//   - below 60s:   "4.1s"   (one decimal)
//   - 60s or more: "1m 05s"
//
// A negative input renders "0ms": a clock that ran backwards is a
// measurement error, and the honest floor is zero, not a sign.
//
// The seconds rung ends at 59950ms, not 60000ms: %.1f would round
// 59950 up to "60.0s", a value the minutes rung owns.
//
// Pure: input in, string out, no I/O and no package state.
func FormatElapsed(ms int) string {
	switch {
	case ms < 0:
		return "0ms"
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 59950:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm %02ds", ms/60000, (ms/1000)%60)
	}
}
