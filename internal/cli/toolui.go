package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// toolRow is a live/completed tool invocation for the status panel.
type toolRow struct {
	Name    string
	Detail  string
	Result  string
	Start   time.Time
	End     time.Time
	Done    bool
	Failed  bool
	Spinner int
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
	// Animate from elapsed time so UI stays lively without external state.
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
)

// renderToolPanel draws active/recent tool rows with icons and elapsed times.
func renderToolPanel(rows []toolRow, width int, now time.Time) string {
	if len(rows) == 0 {
		return ""
	}
	// Show last N tools.
	const maxShow = 8
	start := 0
	if len(rows) > maxShow {
		start = len(rows) - maxShow
	}
	var b strings.Builder
	b.WriteString(toolDimStyle.Render("  tools"))
	b.WriteByte('\n')
	for _, r := range rows[start:] {
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
			detail = r.Result
		}
		detail = truncateStr(detail, max(20, width-28))
		dur := toolTimeStyle.Render(formatDuration(r.elapsed(now)))
		line := fmt.Sprintf("  %s %s %s %s", iconStyled, name, toolDimStyle.Render(detail), dur)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
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
	default:
		return "•"
	}
}
