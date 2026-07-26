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
	toolPathStyle = lipgloss.NewStyle().Reverse(true).Faint(true)
	// GitHub-style diff colors (full-width backgrounds).
	toolDiffAddBg  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(lipgloss.Color("22")) // green on dark green
	toolDiffDelBg  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(lipgloss.Color("88"))  // red on dark red
	toolDiffHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))                                  // cyan header
	toolDiffCtx    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))                                   // dim context
	toolDiffOld    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Faint(true)                       // red (kept for legacy)
	toolDiffNew    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))                                  // green (kept for legacy)
)

// parseToolPath extracts a workspace path from tool Detail/Result text.
// Prefers JSON "path":"..." then "wrote X" / "updated X" prefixes.
func parseToolPath(detail, result string) string {
	for _, s := range []string{detail, result} {
		if p := pathFromJSONField(s); p != "" {
			return p
		}
		if p := pathFromWroteOrUpdated(s); p != "" {
			return p
		}
	}
	return ""
}

func pathFromJSONField(s string) string {
	const key = `"path"`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len(key):])
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	var b strings.Builder
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c == '\\' && j+1 < len(rest) {
			b.WriteByte(rest[j+1])
			j++
			continue
		}
		if c == '"' {
			return b.String()
		}
		b.WriteByte(c)
	}
	return ""
}

func pathFromWroteOrUpdated(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"wrote ", "updated "} {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		rest := s[len(prefix):]
		if j := strings.Index(rest, " ("); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		if j := strings.IndexByte(rest, ' '); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// summarizeToolDetail returns a one-line summary for the collapsed tool row.
// Prefers result first line; strips embedded path when shown separately as a chip.
func summarizeToolDetail(detail, result string) string {
	s := result
	if strings.TrimSpace(s) == "" {
		s = detail
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if p := pathFromWroteOrUpdated(s); p != "" {
		// "wrote path (stats)" → "wrote (stats)"; same for updated.
		if j := strings.Index(s, " ("); j >= 0 {
			if strings.HasPrefix(s, "wrote ") {
				return "wrote " + s[j+1:]
			}
			if strings.HasPrefix(s, "updated ") {
				return "updated " + s[j+1:]
			}
		}
	}
	// Compact JSON detail: drop path field value from summary when present.
	if strings.HasPrefix(s, "{") {
		if p := pathFromJSONField(s); p != "" {
			// Leave raw JSON; path chip carries the path.
			return s
		}
	}
	return s
}

func isEditTool(name string) bool {
	return name == "write_file" || name == "search_replace"
}

// colorDiffLine applies GitHub-style diff coloring to a line.
// Uses dark red/green backgrounds for -/+ lines (full-width) and
// cyan/magenta for headers/hunks.
func colorDiffLine(l string) string {
	trim := l
	switch {
	case strings.HasPrefix(trim, "+++") || strings.HasPrefix(trim, "---"):
		return toolDiffHeader.Render("  " + l)
	case strings.HasPrefix(trim, "@@"):
		return toolDimStyle.Render("  " + l)
	case strings.HasPrefix(trim, "+"):
		return toolDiffAddBg.Render(" " + l)
	case strings.HasPrefix(trim, "-"):
		return toolDiffDelBg.Render(" " + l)
	default:
		return toolDiffCtx.Render(" " + l)
	}
}

// clipPreviewLine truncates a preview line for the terminal width without panicking
// when width is 0 or very small (pre-WindowSizeMsg / narrow panes).
func clipPreviewLine(l string, width int) string {
	// Budget for "    │ " prefix (~6) and ellipsis.
	maxBody := width - 10
	if maxBody < 8 {
		maxBody = 8
	}
	if len(l) <= maxBody {
		return l
	}
	// Keep at least 1 rune of content before "...".
	cut := maxBody - 3
	if cut < 1 {
		cut = 1
	}
	if cut > len(l) {
		cut = len(l)
	}
	return l[:cut] + "..."
}

// renderToolPanel draws tool rows with expand/collapse support.
// logoFrame drives the brand nano-mark for running tools (replaces spinner).
// phase colors the header / running glyphs.
// Returns the rendered string and an approximate line count for layout.
func renderToolPanel(rows []toolRow, width int, now time.Time, selectedIdx int, logoFrame int, phase brandPhase) (string, int) {
	if len(rows) == 0 {
		return "", 0
	}
	if width < 20 {
		width = 20
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

	open, done, total := countTools(rows)
	hdrColor := brandColor(phase)
	if phase != phaseTools && phase != phaseMulti {
		hdrColor = brandColorTools
	}
	hdrMark := brandGlyph(logoFrame, hdrColor)
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hdrColor)).Render(
		fmt.Sprintf("  %s tools  %d/%d done · %d active", hdrMark, done, total, open),
	))
	b.WriteByte('\n')
	totalLines++

	// Group: running first, then failed, then ok (stable within groups by original order).
	type indexed struct {
		i int
		r toolRow
	}
	var running, failed, okRows []indexed
	for idx, r := range visible {
		it := indexed{i: idx + start, r: r}
		switch {
		case !r.Done:
			running = append(running, it)
		case r.Failed:
			failed = append(failed, it)
		default:
			okRows = append(okRows, it)
		}
	}
	ordered := append(append(running, failed...), okRows...)

	for _, it := range ordered {
		r := it.r
		idx := it.i - start
		var iconStyled string
		switch {
		case !r.Done:
			// 1-cell braille diamond pulse — phase-colored.
			iconStyled = brandGlyph(logoFrame+idx, brandColorTools)
		case r.Failed:
			iconStyled = toolErrStyle.Render("✗")
		default:
			iconStyled = toolOkStyle.Render("✓")
		}

		kindIcon := toolIconForName(r.Name)
		name := toolNameStyle.Render(r.Name)

		path := parseToolPath(r.Detail, r.Result)
		pathPart := ""
		if path != "" {
			// Path chip: reverse video dim.
			chip := path
			if len(chip) > max(12, width/3) {
				chip = "…" + chip[len(chip)-max(11, width/3-1):]
			}
			pathPart = " " + toolPathStyle.Render(" "+chip+" ")
		}

		summary := summarizeToolDetail(r.Detail, "")
		if r.Done && r.Result != "" {
			summary = summarizeToolDetail(r.Detail, r.Result)
		}
		// Avoid duplicating path in the summary line when chip is shown.
		if path != "" && summary == path {
			summary = ""
		}
		summary = truncateStr(summary, max(16, width-40-len(path)))
		summaryStyled := toolDimStyle.Render(summary)
		durStr := toolTimeStyle.Render(formatDuration(r.elapsed(now)))

		// Collapsed: brand/status · kind · name · [path chip] · summary · duration
		line := fmt.Sprintf("  %s %s %s%s %s %s", iconStyled, kindIcon, name, pathPart, summaryStyled, durStr)
		if selectedIdx == it.i {
			line = toolSelStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		totalLines++

		// Expanded preview: show input args and output result, each capped at maxPreviewLines.
		if r.Expanded {
			maxPreviewLines := 7
			if isEditTool(r.Name) {
				maxPreviewLines = 16
			}

			// Input section.
			if r.Detail != "" {
				b.WriteString(toolSection.Render("    ╭─ input"))
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
					l = clipPreviewLine(l, width)
					b.WriteString(fmt.Sprintf("    │ %s", l))
					b.WriteByte('\n')
					totalLines++
				}
			}

			// Output section.
			if r.Result != "" {
				b.WriteString(toolSection.Render("    ╰─ output"))
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
				colorDiff := isEditTool(r.Name) || resultLooksLikeDiff(r.Result)
				for _, l := range lines {
					l = clipPreviewLine(l, width)
					if colorDiff {
						// GitHub-style: drop │, use full-width colored background instead.
						b.WriteString("  " + colorDiffLine(l))
					} else {
						b.WriteString(fmt.Sprintf("    │ %s", l))
					}
					b.WriteByte('\n')
					totalLines++
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), totalLines
}

func resultLooksLikeDiff(result string) bool {
	return strings.Contains(result, "\n--- ") || strings.Contains(result, "\n+++ ") ||
		strings.HasPrefix(result, "--- ") || strings.HasPrefix(result, "+++ ") ||
		strings.Contains(result, "\n--- a/") || strings.Contains(result, "\n+++ b/") ||
		strings.HasPrefix(result, "--- a/") || strings.HasPrefix(result, "+++ b/")
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
