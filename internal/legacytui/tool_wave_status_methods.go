package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"time"
)

// refreshLiveToolWaveStatus updates the latest work-status ChatBlock and
// stepDetail with live k/n progress so long tool batches do not look hung.
func (m *TUIModel) refreshLiveToolWaveStatus() {
	open, done, total := cli.ToolWaveCounts(m.toolRows)
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
	summary := cli.FormatLiveToolWaveSummary(open, done, total, elapsed)
	if summary == "" {
		return
	}
	// Composer footer: prefer explicit k/n so heartbeats and counts stay aligned.
	m.stepDetail = summary
	m.stepDetailAt = time.Now()
	m.stalledWarning = false

	// Rewrite the latest live work-status block summary (keep detail lines).
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if !cli.IsWorkStatusBlock(m.blocks[i]) {
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
