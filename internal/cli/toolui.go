package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// toolRow is a live/completed tool invocation for the status panel.
type toolRow struct {
	Name     string
	Detail   string // input arguments (truncated)
	Result   string // output result (may be large)
	Start    time.Time
	End      time.Time
	Done     bool
	Failed   bool
	Expanded bool // show full I/O preview
}

func (r toolRow) elapsed(now time.Time) time.Duration {
	if r.Done {
		if r.End.IsZero() {
			return 0
		}
		return r.End.Sub(r.Start)
	}
	if r.Start.IsZero() {
		return 0
	}
	return now.Sub(r.Start)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (r toolRow) icon(now time.Time) string {
	if r.Done {
		if r.Failed {
			return "✗"
		}
		return "✓"
	}
	idx := int(now.UnixMilli()/80) % len(spinnerFrames)
	return spinnerFrames[idx]
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

var (
	toolRunStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	toolOkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	toolErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	toolNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	toolDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	toolTimeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	toolSelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("237"))
	toolSection   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Faint(true)
)

// renderToolPanel draws tool rows with expand/collapse support.
// selectedIdx is the index of the currently selected tool (-1 for none).
// Width limits the output area.
// Returns the rendered string and an approximate line count for layout.
func renderToolPanel(rows []toolRow, width int, now time.Time, selectedIdx int) (string, int) {
	if len(rows) == 0 {
		return "", 0
	}
	// Show last N tools.
	const maxShow = 20
	start := 0
	if len(rows) > maxShow {
		start = len(rows) - maxShow
	}
	visible := rows[start:]
	var b strings.Builder
	totalLines := 0

	b.WriteString(toolDimStyle.Render(fmt.Sprintf("  tools (%d)", len(rows))))
	b.WriteByte('\n')
	totalLines++

	for idx, r := range visible {
		icon := r.icon(now)
		var iconStyled string
		switch {
		case !r.Done:
			iconStyled = toolRunStyle.Render(icon)
		case r.Failed:
			iconStyled = toolErrStyle.Render(icon)
		default:
			iconStyled = toolOkStyle.Render(icon)
		}

		name := toolNameStyle.Render(r.Name)
		detail := r.Detail
		if r.Done && r.Result != "" {
			detail = firstLine(r.Result, r.Detail)
		}
		detail = truncateStr(detail, max(20, width-32))
		durStr := toolTimeStyle.Render(formatDuration(r.elapsed(now)))

		// Selection highlight.
		line := fmt.Sprintf("  %s %s %s %s", iconStyled, name, toolDimStyle.Render(detail), durStr)
		if selectedIdx == idx+start {
			line = toolSelStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		totalLines++

		// Expanded preview: show input args and output result, each capped at maxPreviewLines.
		if r.Expanded {
			const maxPreviewLines = 7

			// Input section.
			if r.Detail != "" {
				b.WriteString(toolSection.Render(fmt.Sprintf("    ╭─ input")))
				b.WriteByte('\n')
				totalLines++
				inputLines := strings.Split(r.Detail, "\n")
				lines := inputLines
				if len(lines) > maxPreviewLines {
					lines = lines[len(lines)-maxPreviewLines:]
					b.WriteString(toolDimStyle.Render(fmt.Sprintf("    │ … (%d more)", len(inputLines)-maxPreviewLines)))
					b.WriteByte('\n')
					totalLines++
				}
				for _, l := range lines {
					if len(l) > width-10 {
						l = l[:width-13] + "..."
					}
					b.WriteString(fmt.Sprintf("    │ %s", l))
					b.WriteByte('\n')
					totalLines++
				}
			}

			// Output section.
			if r.Result != "" && len(r.Result) > 0 {
				b.WriteString(toolSection.Render(fmt.Sprintf("    ╰─ output")))
				b.WriteByte('\n')
				totalLines++
				resultLines := strings.Split(r.Result, "\n")
				lines := resultLines
				if len(lines) > maxPreviewLines {
					lines = lines[len(lines)-maxPreviewLines:]
					b.WriteString(toolDimStyle.Render(fmt.Sprintf("    │ … (%d more)", len(resultLines)-maxPreviewLines)))
					b.WriteByte('\n')
					totalLines++
				}
				for _, l := range lines {
					if len(l) > width-10 {
						l = l[:width-13] + "..."
					}
					b.WriteString(fmt.Sprintf("    │ %s", l))
					b.WriteByte('\n')
					totalLines++
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), totalLines
}

// toolIconForName picks a small glyph per tool kind.
func toolIconForName(name string) string {
	switch name {
	case "read_file":
		return "📖"
	case "list_dir":
		return "📂"
	case "grep", "glob":
		return "🔎"
	case "write_file", "search_replace":
		return "✎"
	case "run_command":
		return "▸"
	case "search":
		return "🌐"
	default:
		return "•"
	}
}
