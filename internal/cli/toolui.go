package cli

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// toolRow is a live/completed tool invocation for the status panel.
type toolRow struct {
	ToolCallID string
	Name       string
	// Agent is the subagent that ran this tool ("" = the session's own call).
	Agent string
	// Detail is argument preview (JSON or redacted input). Never lifecycle text.
	Detail string
	// Status is queued|running|completed|failed (operator-facing lifecycle).
	Status   string
	Result   string // output result (may be large)
	Start    time.Time
	End      time.Time
	Done     bool
	Failed   bool
	Expanded bool // show full I/O preview
}

// toolRenderItem is the bounded, presentation-neutral view shared by live and history renderers.
type toolRenderItem struct {
	Name, Detail, Result string
	Done, Failed         bool
}
type toolRenderOptions struct{ ASCII, Color bool }

func terminalToolRenderOptions() toolRenderOptions {
	term := strings.ToLower(os.Getenv("TERM"))
	plain := os.Getenv("NO_COLOR") != "" || term == "dumb"
	return toolRenderOptions{ASCII: term == "dumb", Color: !plain}
}

func newToolRenderItem(name, detail, result string, done, failed bool) toolRenderItem {
	return toolRenderItem{Name: name, Detail: detail, Result: result, Done: done, Failed: failed}
}

func (t toolRenderItem) statusIcon(ascii bool) string {
	if !t.Done {
		if ascii {
			return ">"
		}
		return "◐"
	}
	if t.Failed {
		if ascii {
			return "!"
		}
		return "✗"
	}
	if ascii {
		return "*"
	}
	return "✓"
}

func (t toolRenderItem) summary(max int) string {
	s := summarizeToolDetail(t.Name, t.Detail, t.Result)
	if p := parseToolPath(t.Detail, t.Result); p != "" && s == p {
		s = ""
	}
	return boundedToolText(s, max)
}

func boundedToolText(s string, max int) string {
	if max < 1 {
		max = 1
	}
	s = strings.ReplaceAll(redactPreview(strings.TrimSpace(s)), "\n", " ")
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func formatToolLine(t toolRenderItem, width int, opts toolRenderOptions) string {
	// Leave room for optional lifecycle badge (running/queued).
	status, summary := t.statusIcon(opts.ASCII), t.summary(max(12, width-32))
	kind := toolKindIcon(t.Name, opts.ASCII)
	if !opts.Color {
		return fmt.Sprintf("  %s %s %s %s", status, kind, t.Name, summary)
	}
	if t.Failed {
		status = toolErrStyle.Render(status)
	} else if t.Done {
		status = toolOkStyle.Render(status)
	}
	return fmt.Sprintf("  %s %s %s %s", status, kind, toolNameStyle.Render(t.Name), toolDimStyle.Render(summary))
}

// formatToolPanelLine is the colored panel row: icon kind name [status] summary elapsed.
func formatToolPanelLine(r toolRow, iconStyled string, width int, now time.Time, selected bool) string {
	path := parseToolPath(r.Detail, r.Result)
	pathPart := ""
	if path != "" {
		chip := path
		if len(chip) > max(12, width/3) {
			chip = "…" + chip[len(chip)-max(11, width/3-1):]
		}
		pathPart = " " + toolPathStyle.Render(" "+chip+" ")
	}
	item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
	budget := max(12, width-48-len(path))
	summary := item.summary(budget)
	if path != "" && summary == path {
		summary = ""
	}
	statusPart := ""
	if st := strings.TrimSpace(r.Status); st != "" && !r.Done {
		statusPart = " " + toolDimStyle.Render(st)
	}
	marker := "  "
	if selected {
		marker = "▸ "
	}
	// Nested tools carry a ◆ agent badge so parallel subagents stay
	// distinguishable from the session's own calls.
	agentPart := ""
	if r.Agent != "" {
		agentPart = agentBadgeStyle.Render("◆ "+r.Agent) + " "
	}
	line := fmt.Sprintf("%s%s %s %s%s%s %s %s",
		marker, iconStyled, toolKindIcon(r.Name, false), agentPart+toolNameStyle.Render(r.Name),
		statusPart, pathPart, toolDimStyle.Render(summary), toolTimeStyle.Render(formatDuration(r.elapsed(now))),
	)
	if selected {
		line = toolSelStyle.Render(line)
	}
	return line
}

// toolKindIcon returns a glyph for the tool name. ASCII terminals get a
// single-byte stand-in so dumb TERM / NO_COLOR still show tool kind.
func toolKindIcon(name string, ascii bool) string {
	if !ascii {
		return toolIconForName(name)
	}
	switch name {
	case "read_file":
		return "r"
	case "list_dir":
		return "d"
	case "grep", "glob":
		return "/"
	case "write_file", "search_replace":
		return "e"
	case "run_command":
		return ">"
	case "search":
		return "w"
	case "delegate", "dispatch_tasks":
		return "+"
	default:
		return "-"
	}
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
	// Diff stat colors (foreground only — the ± counts on edit rows).
	toolDiffAdd = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	toolDiffDel = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	// agentBadgeStyle marks nested tool rows with their producing subagent
	// (◆ = the brand's agent glyph, magenta = the multi/parallel phase color).
	agentBadgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorMulti))
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
// Prefers operator-facing intent for delegate/dispatch; else result first line.
func summarizeToolDetail(name, detail, result string) string {
	if s := summarizeAgentTool(name, detail, result); s != "" {
		return s
	}
	s := result
	if strings.TrimSpace(s) == "" {
		s = detail
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// Lifecycle tokens alone are not useful as the only summary.
	if isLifecycleStatus(s) {
		return ""
	}
	if p := pathFromWroteOrUpdated(s); p != "" {
		if j := strings.Index(s, " ("); j >= 0 {
			if strings.HasPrefix(s, "wrote ") {
				return "wrote " + s[j+1:]
			}
			if strings.HasPrefix(s, "updated ") {
				return "updated " + s[j+1:]
			}
		}
	}
	if strings.HasPrefix(s, "{") {
		if p := pathFromJSONField(s); p != "" {
			return s
		}
	}
	return s
}

func isLifecycleStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "queued", "running", "completed", "failed", "completed (truncated)", "failed (truncated)":
		return true
	default:
		return false
	}
}

func lifecycleStatusFailed(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "failed")
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
	return truncatePreviewUTF8(l, cut) + "..."
}

func truncatePreviewUTF8(s string, maxBytes int) string {
	if maxBytes >= len(s) {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// renderToolPanel is the legacy entry point used by tests/benches.
// Delegates to the windowed panel (max 6 rows, active+recent first).
// Expand previews only paint for the selected row; if selectedIdx < 0 and a row
// is Expanded, the first Expanded row is selected so legacy tests still see I/O.
func renderToolPanel(rows []toolRow, width int, now time.Time, selectedIdx int, logoFrame int, phase brandPhase) (string, int) {
	st := toolPanelState{Selected: selectedIdx, Focused: selectedIdx >= 0}
	if selectedIdx < 0 {
		for i := range rows {
			if rows[i].Expanded {
				st.Selected = i
				break
			}
		}
	}
	out, n, _ := renderToolPanelWindow(rows, width, now, st, logoFrame, phase, toolMaxVisibleRows, 0)
	return out, n
}

func resultLooksLikeDiff(result string) bool {
	return strings.Contains(result, "\n--- ") || strings.Contains(result, "\n+++ ") ||
		strings.HasPrefix(result, "--- ") || strings.HasPrefix(result, "+++ ") ||
		strings.Contains(result, "\n--- a/") || strings.Contains(result, "\n+++ b/") ||
		strings.HasPrefix(result, "--- a/") || strings.HasPrefix(result, "+++ b/")
}

// toolIconForName picks the typed action glyph for a tool: ⚙ tool, ◆ agent.
// Single-width text only — emoji misalign columns (see action.go).
func toolIconForName(name string) string {
	return actionIconForTool(name)
}
