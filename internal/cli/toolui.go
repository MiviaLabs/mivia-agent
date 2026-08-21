package cli

import (
	"encoding/json"
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

// NewToolRenderItem builds a bounded, presentation-neutral tool render item.
// Shared by the live status panel and the classic-mode renderer in
// internal/clichat.
func NewToolRenderItem(name, detail, result string, done, failed bool) toolRenderItem {
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
		return GlyphCross
	}
	if ascii {
		return "*"
	}
	return GlyphCheck
}

func (t toolRenderItem) summary(max int) string {
	s := summarizeToolDetail(t.Name, t.Detail, t.Result)
	if p := ParseToolPath(t.Detail, t.Result); p != "" && s == p {
		s = ""
	}
	return BoundedToolText(s, max)
}

// BoundedToolText sanitizes and bounds model-influenced tool text (names,
// arguments, error bodies) to max runes. Shared by the live status panel and
// the classic-mode renderer in internal/clichat.
func BoundedToolText(s string, max int) string {
	if max < 1 {
		max = 1
	}
	// Every classic tool row - start, end, panel, history - funnels
	// model-influenced text (tool names, arguments, error bodies) through here,
	// so this is the chokepoint that has to strip ANSI and NUL. The TUI path
	// already sanitized via SafeChatBlockText; the classic path did not, and a
	// tool error carrying ESC[2J reached the terminal raw.
	s = strings.ReplaceAll(RedactPreview(SafeChatBlockText(strings.TrimSpace(s), 0)), "\n", " ")
	// Bound by runes, not bytes: a byte slice can split a multibyte rune.
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func formatToolLine(t toolRenderItem, width int, opts toolRenderOptions) string {
	// Leave room for optional lifecycle badge (running/queued).
	status, summary := t.statusIcon(opts.ASCII), t.summary(Max(12, width-32))
	kind := toolKindIcon(t.Name, opts.ASCII)
	if !opts.Color {
		return fmt.Sprintf("  %s %s %s %s", status, kind, t.Name, summary)
	}
	if t.Failed {
		status = ToolErrStyle.Render(status)
	} else if t.Done {
		status = ToolOkStyle.Render(status)
	}
	return fmt.Sprintf("  %s %s %s %s", status, kind, ToolNameStyle.Render(t.Name), ToolDimStyle.Render(summary))
}

// formatToolPanelLine is the colored panel row: icon kind name [status] summary elapsed.
func formatToolPanelLine(r toolRow, iconStyled string, width int, now time.Time, selected bool) string {
	path := ParseToolPath(r.Detail, r.Result)
	pathPart := ""
	if path != "" {
		chip := path
		if len(chip) > Max(12, width/3) {
			chip = "…" + chip[len(chip)-Max(11, width/3-1):]
		}
		pathPart = " " + ToolPathStyle.Render(" "+chip+" ")
	}
	item := NewToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
	budget := Max(12, width-48-len(path))
	summary := item.summary(budget)
	if path != "" && summary == path {
		summary = ""
	}
	statusPart := ""
	if st := strings.TrimSpace(r.Status); st != "" && !r.Done {
		statusPart = " " + ToolDimStyle.Render(st)
	}
	marker := "  "
	if selected {
		marker = GlyphTriR + " "
	}
	// Nested tools carry a ◆ agent badge so parallel subagents stay
	// distinguishable from the session's own calls.
	agentPart := ""
	if r.Agent != "" {
		agentPart = AgentBadgeStyle.Render(GlyphDiamond+" "+r.Agent) + " "
	}
	line := fmt.Sprintf("%s%s %s %s%s%s %s %s",
		marker, iconStyled, toolKindIcon(r.Name, false), agentPart+ToolNameStyle.Render(r.Name),
		statusPart, pathPart, ToolDimStyle.Render(summary), ToolTimeStyle.Render(FormatDuration(r.elapsed(now))),
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
		return ToolIconForName(name)
	}
	switch name {
	case "read_file":
		return "r"
	case "list_dir":
		return "d"
	case "grep", "glob":
		return "/"
	case "write_file", "search_replace", "multi_edit":
		return "e"
	case "run_command":
		return ">"
	case "search":
		return "w"
	case HandlerDelegate, ToolDispatchTasks:
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
			return GlyphCross
		}
		return GlyphCheck
	}
	idx := int(now.UnixMilli()/80) % len(spinnerFrames)
	return spinnerFrames[idx]
}

// FormatDuration renders a duration as tool-elapsed text (ms/s/m:ss). Shared
// by the live status panel and the classic-mode renderer in internal/clichat.
func FormatDuration(d time.Duration) string {
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
	toolRunStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))
	// ToolOkStyle renders a completed, non-failed tool status icon. Shared by
	// the classic-mode renderer in internal/clichat.
	ToolOkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDiffAdd))
	// ToolNameStyle renders a tool's name. Shared by the classic-mode
	// renderer in internal/clichat.
	ToolNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true)
	// ToolTimeStyle renders a tool's elapsed-time text. Shared by the
	// classic-mode renderer in internal/clichat.
	ToolTimeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorTime))
	toolSelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorBright)).Background(lipgloss.Color(themeColorSelBg))
	toolSection   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Faint(true)
	// ToolPathStyle renders the workspace-path chip on a tool row. Shared by
	// the classic-mode renderer in internal/clichat.
	ToolPathStyle = lipgloss.NewStyle().Reverse(true).Faint(true)
	// AgentBadgeStyle marks nested tool rows with their producing subagent
	// (◆ = the brand's agent glyph, magenta = the multi/parallel phase
	// color). Shared by the classic-mode renderer in internal/clichat.
	AgentBadgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorMulti))
	// GitHub-style diff colors (full-width backgrounds).
	toolDiffAddBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDiffAdd)).Background(lipgloss.Color(themeColorDiffAddBg)) // green on dark green
	toolDiffDelBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDiffDel)).Background(lipgloss.Color(themeColorDiffDelBg)) // red on dark red
	toolDiffHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))                                                    // cyan header
	toolDiffOld    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDiffDel)).Faint(true)                                     // red (kept for legacy)
	toolDiffNew    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDiffAdd))                                                 // green (kept for legacy)
)

// ParseToolPath extracts a workspace path from tool Detail/Result text.
// Prefers JSON "path":"..." then "wrote X" / "updated X" prefixes.
func ParseToolPath(detail, result string) string {
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
	if IsLifecycleStatus(s) {
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

// IsLifecycleStatus reports whether s is a bare lifecycle token
// (queued/running/completed/failed, with truncated/duplicate variants) and
// not a useful summary on its own. Shared by the classic-mode renderer in
// internal/clichat.
func IsLifecycleStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "queued", "running", "completed", "failed", "completed (truncated)", "failed (truncated)", "completed (duplicate)", "failed (duplicate)":
		return true
	default:
		return false
	}
}

func lifecycleStatusFailed(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "failed")
}

// IsEditTool reports whether name is a file-editing tool (write_file,
// search_replace, multi_edit). Shared by the classic-mode renderer in
// internal/clichat.
func IsEditTool(name string) bool {
	return name == "write_file" || name == "search_replace" || name == "multi_edit"
}

// ColorDiffLine is a thin alias of renderDiffLine for call-site compatibility
// (tool preview / renderDiffBody). @@ hunks use magenta (unified with markdown
// and highlight surfaces), not dim.
func ColorDiffLine(l string) string {
	return RenderDiffLine(l)
}

// ClipPreviewLine truncates a preview line for the terminal width without panicking
// when width is 0 or very small (pre-WindowSizeMsg / narrow panes).
func ClipPreviewLine(l string, width int) string {
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
	return TruncatePreviewUTF8(l, cut) + "..."
}

// TruncatePreviewUTF8 cuts s to at most maxBytes bytes, backing off until the
// cut point lands on a valid UTF-8 boundary. Shared by TUI preview text and
// tool-error name formatting.
func TruncatePreviewUTF8(s string, maxBytes int) string {
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

// ResultLooksLikeDiff reports whether result carries unified-diff markers
// (---/+++ headers). Shared by the classic-mode renderer in internal/clichat.
func ResultLooksLikeDiff(result string) bool {
	return strings.Contains(result, "\n--- ") || strings.Contains(result, "\n+++ ") ||
		strings.HasPrefix(result, "--- ") || strings.HasPrefix(result, "+++ ") ||
		strings.Contains(result, "\n--- a/") || strings.Contains(result, "\n+++ b/") ||
		strings.HasPrefix(result, "--- a/") || strings.HasPrefix(result, "+++ b/")
}

// ToolIconForName picks the typed action glyph for a tool: ⚙ tool, ◆ agent.
// Single-width text only - emoji misalign columns (see action.go).
func ToolIconForName(name string) string {
	return ActionIconForTool(name)
}

// summarizeAgentTool builds operator-facing one-liners for delegation tools.
// Relocated from toolui_agent.go: this file is its sole caller.
func summarizeAgentTool(name, detail, result string) string {
	switch name {
	case HandlerDelegate:
		return summarizeDelegate(detail, result)
	case ToolDispatchTasks:
		return summarizeDispatchTasks(detail, result)
	default:
		return ""
	}
}

func summarizeDelegate(detail, result string) string {
	task, multi := extractDelegateArgs(detail)
	if task == "" && strings.TrimSpace(result) != "" {
		// Prefer short status/output from completed body.
		if st, out := extractJSONStatusOutput(result); st != "" || out != "" {
			if out != "" {
				return clipOneLine(out, 80)
			}
			return st
		}
		return clipOneLine(firstLineOnly(result), 80)
	}
	mode := handlerOneshot
	if multi {
		mode = handlerMultiStep
	}
	if task == "" {
		return mode
	}
	return mode + ": " + clipOneLine(task, 72)
}

func summarizeDispatchTasks(detail, result string) string {
	n, preview := extractDispatchTasksArgs(detail)
	if n == 0 && strings.TrimSpace(result) != "" {
		if st, out := extractJSONStatusOutput(result); out != "" {
			return clipOneLine(out, 80)
		} else if st != "" {
			return st
		}
		// Array of task results
		if c := countJSONArray(result); c > 0 {
			return fmt.Sprintf("%d task results", c)
		}
		return clipOneLine(firstLineOnly(result), 80)
	}
	if n <= 0 {
		return "batch"
	}
	if preview != "" {
		return fmt.Sprintf("%d tasks · %s", n, clipOneLine(preview, 60))
	}
	return fmt.Sprintf("%d tasks", n)
}

func extractDelegateArgs(detail string) (task string, multi bool) {
	var in struct {
		Task      string `json:"task"`
		MultiStep bool   `json:"multi_step"`
	}
	if json.Unmarshal([]byte(detail), &in) != nil {
		return "", false
	}
	return strings.TrimSpace(in.Task), in.MultiStep
}

func extractDispatchTasksArgs(detail string) (n int, firstPrompt string) {
	var in struct {
		Tasks []struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
		} `json:"tasks"`
	}
	if json.Unmarshal([]byte(detail), &in) != nil {
		return 0, ""
	}
	n = len(in.Tasks)
	if n > 0 {
		firstPrompt = strings.TrimSpace(in.Tasks[0].Prompt)
		if firstPrompt == "" {
			firstPrompt = strings.TrimSpace(in.Tasks[0].ID)
		}
	}
	return n, firstPrompt
}

func extractJSONStatusOutput(s string) (status, output string) {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return "", ""
	}
	if v, ok := m["status"].(string); ok {
		status = v
	}
	if v, ok := m["output"].(string); ok {
		output = v
	}
	if v, ok := m["error"].(string); ok && output == "" {
		output = v
	}
	return status, output
}

func countJSONArray(s string) int {
	var a []any
	if json.Unmarshal([]byte(s), &a) != nil {
		return 0
	}
	return len(a)
}

func firstLineOnly(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func clipOneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	return BoundedToolText(s, max)
}
