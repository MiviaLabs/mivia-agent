package cli

import (
	"fmt"
	"time"
)

// toolWaveCounts returns open/done/total for live toolRows (excludes banners).
func toolWaveCounts(rows []toolRow) (open, done, total int) {
	for _, r := range rows {
		if isBannerTool(r.Name) {
			continue
		}
		total++
		if r.Done {
			done++
		} else {
			open++
		}
	}
	return open, done, total
}

// formatLiveToolWaveSummary builds the live one-line wave status.
// Examples: "Running 2 tools… · 0/2 done · 3s", "Working · 1/3 done · 12s"
func formatLiveToolWaveSummary(open, done, total int, elapsed time.Duration) string {
	if total <= 0 {
		return ""
	}
	sec := elapsed.Round(time.Second)
	if sec < 0 {
		sec = 0
	}
	if open == 0 && done == total {
		return capRunes(fmt.Sprintf("Used %d tools · %d/%d done · %s", total, done, total, sec), toolStatusMaxRunes+16)
	}
	if total == 1 {
		return capRunes(fmt.Sprintf("Running 1 tool… · %d/%d done · %s", done, total, sec), toolStatusMaxRunes+16)
	}
	return capRunes(fmt.Sprintf("Running %d tools… · %d/%d done · %s", total, done, total, sec), toolStatusMaxRunes+16)
}
