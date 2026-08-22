package cli

import (
	"encoding/json"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// ToolRow is a live/completed tool invocation for the status panel. Relocated
// from internal/legacytui/toolui.go: needed unqualified there (its own
// rendering) and here (tool-wave counting). internal/legacytui keeps a type
// alias so its own call sites are unchanged.
type ToolRow struct {
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

// ToolRenderItem is the bounded, presentation-neutral view shared by live and
// history renderers.
type ToolRenderItem struct {
	Name, Detail, Result string
	Done, Failed         bool
}

// NewToolRenderItem builds a bounded, presentation-neutral tool render item.
// Shared by the live status panel (internal/legacytui) and the classic-mode
// renderer.
func NewToolRenderItem(name, detail, result string, done, failed bool) ToolRenderItem {
	return ToolRenderItem{Name: name, Detail: detail, Result: result, Done: done, Failed: failed}
}

// StatusIcon renders the item's lifecycle glyph (queued/done/failed).
// Exported: internal/legacytui's formatToolLine/formatToolPanelLine call it
// on a ToolRenderItem value returned from this package.
func (t ToolRenderItem) StatusIcon(ascii bool) string {
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

// Summary renders the item's bounded one-line preview text. Exported: see
// StatusIcon.
func (t ToolRenderItem) Summary(max int) string {
	s := summarizeToolDetail(t.Name, t.Detail, t.Result)
	if p := ParseToolPath(t.Detail, t.Result); p != "" && s == p {
		s = ""
	}
	return BoundedToolText(s, max)
}

// RedactPreview redacts secrets from preview text. Relocated from
// internal/legacytui/toolpanel.go: needed unqualified there and by the
// classic-mode renderer here.
func RedactPreview(s string) string { return redact.Text(s) }

// BoundedToolText sanitizes and bounds model-influenced tool text (names,
// arguments, error bodies) to max runes. Shared by the live status panel
// (internal/legacytui) and the classic-mode renderer.
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

// FormatDuration renders a duration as tool-elapsed text (ms/s/m:ss). Shared
// by the live status panel (internal/legacytui) and the classic-mode
// renderer.
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
// not a useful summary on its own. Shared by the classic-mode renderer.
func IsLifecycleStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "queued", "running", "completed", "failed", "completed (truncated)", "failed (truncated)", "completed (duplicate)", "failed (duplicate)":
		return true
	default:
		return false
	}
}

// LifecycleStatusFailed reports whether s is a "failed" lifecycle token.
// Exported: relocated from internal/legacytui/toolui.go's lowercase
// lifecycleStatusFailed, needed there by tui_tools_apply_methods.go.
func LifecycleStatusFailed(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "failed")
}

// IsEditTool reports whether name is a file-editing tool (write_file,
// search_replace, multi_edit). Shared by the classic-mode renderer.
func IsEditTool(name string) bool {
	return name == "write_file" || name == "search_replace" || name == "multi_edit"
}

// ColorDiffLine is a thin alias of RenderDiffLine for call-site compatibility
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

// ResultLooksLikeDiff reports whether result carries unified-diff markers
// (---/+++ headers). Shared by the classic-mode renderer.
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
func summarizeAgentTool(name, detail, result string) string {
	switch name {
	case cliorchestrate.HandlerDelegate:
		return summarizeDelegate(detail, result)
	case cliorchestrate.ToolDispatchTasks:
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
	mode := cliorchestrate.HandlerOneshot
	if multi {
		mode = cliorchestrate.HandlerMultiStep
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
