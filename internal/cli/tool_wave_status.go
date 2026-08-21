package cli

import (
	"fmt"
	"strings"
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

// refreshLiveToolWaveStatus updates the latest work-status ChatBlock and
// stepDetail with live k/n progress so long tool batches do not look hung.
func (m *tuiModel) refreshLiveToolWaveStatus() {
	open, done, total := toolWaveCounts(m.toolRows)
	// Prefer wave counters: finished tools leave toolRows, so open-only counts lie.
	if m.toolWaveTotal > 0 {
		total = m.toolWaveTotal
		done = m.toolWaveDone
		if done > total {
			done = total
		}
		open = total - done
		if open < 0 {
			open = 0
		}
	}
	if total == 0 {
		return
	}
	elapsed := time.Duration(0)
	if !m.turnStart.IsZero() {
		elapsed = time.Since(m.turnStart)
	}
	summary := formatLiveToolWaveSummary(open, done, total, elapsed)
	if summary == "" {
		return
	}
	// Composer footer: prefer explicit k/n so heartbeats and counts stay aligned.
	m.stepDetail = summary
	m.stepDetailAt = time.Now()
	m.stalledWarning = false

	// Rewrite the latest live work-status block summary (keep detail lines).
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if !IsWorkStatusBlock(m.blocks[i]) {
			continue
		}
		body := strings.TrimSpace(m.blocks[i].Text)
		body = strings.TrimPrefix(body, "→")
		body = strings.TrimSpace(body)
		var detailLines []string
		if parts := strings.Split(body, "\n"); len(parts) > 1 {
			for _, p := range parts[1:] {
				p = strings.TrimSpace(p)
				if p != "" {
					detailLines = append(detailLines, p)
				}
			}
		}
		// Rebuild: new summary + prior per-tool lines (if any).
		var b strings.Builder
		b.WriteString("→ ")
		b.WriteString(summary)
		for _, line := range detailLines {
			b.WriteByte('\n')
			b.WriteString(line)
		}
		m.blocks[i].Text = b.String()
		m.blocks[i].Rendered = TUIDimStyle.Render("  → " + summary)
		// Keep Collapsed as-is (multi-tool starts collapsed).
		if m.waiting {
			m.renderVP()
		}
		return
	}
}
