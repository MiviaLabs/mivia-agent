package render

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FormatToolOutput formats raw tool outputs (commands, grep searches, file reads, JSON payloads)
// into clean, structured, readable transcript lines styled through theme roles.
func FormatToolOutput(t theme.Theme, tier theme.Tier, name, output string, ok bool, width int) (detail string, body []string, collapsible bool) {
	if output == "" {
		return "", nil, false
	}
	lower := strings.ToLower(name)

	switch {
	case isCommandTool(lower):
		body, collapsible = FormatCommandOutput(t, tier, output, ok, width)
		return "", body, collapsible
	case isSearchTool(lower):
		summary, body := FormatGrepOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isReadTool(lower):
		body, collapsible = FormatFileReadOutput(t, tier, output, width)
		return "", body, collapsible
	case isJSONPayload(output):
		body = FormatJSONOutput(t, tier, output, width)
		return "", body, len(body) > 6
	default:
		rawLines := strings.Split(output, "\n")
		return "", rawLines, len(rawLines) > 8
	}
}

func isCommandTool(lower string) bool {
	return lower == "run_command" || lower == "bash" || lower == "terminal" || lower == "exec" || lower == "command"
}

func isSearchTool(lower string) bool {
	return strings.Contains(lower, "grep") || strings.Contains(lower, "find") || strings.Contains(lower, "glob") || strings.Contains(lower, "symbol")
}

func isReadTool(lower string) bool {
	return lower == "read_file" || lower == "view_file" || lower == "list_dir"
}

func isJSONPayload(s string) bool {
	trimmed := strings.TrimSpace(s)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// FormatCommandOutput formats command stdout/stderr. Long successful logs are tailed;
// failure logs highlight error/panic lines.
func FormatCommandOutput(t theme.Theme, tier theme.Tier, output string, ok bool, width int) ([]string, bool) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= 6 {
		return lines, false
	}

	subtle := Role(t, tier, theme.RoleFGSubtle)
	danger := Role(t, tier, theme.RoleDanger)

	if !ok {
		// Highlight failure lines in danger role
		var out []string
		for _, l := range lines {
			lower := strings.ToLower(l)
			if strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") {
				out = append(out, danger.Render(l))
			} else {
				out = append(out, l)
			}
		}
		return out, true
	}

	// For successful long output, show first 2 + last 4 lines
	if len(lines) > 8 {
		hidden := len(lines) - 5
		var out []string
		out = append(out, lines[0])
		out = append(out, subtle.Render(fmt.Sprintf("... %d lines omitted ...", hidden)))
		out = append(out, lines[len(lines)-4:]...)
		return out, true
	}

	return lines, true
}

type grepJSONMatch struct {
	File        string `json:"File"`
	LineNumber  int    `json:"LineNumber"`
	LineContent string `json:"LineContent"`
}

// FormatGrepOutput groups grep/search results by file.
func FormatGrepOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	var matches []grepJSONMatch

	// Try parsing JSON lines format
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			var m grepJSONMatch
			if err := json.Unmarshal([]byte(trimmed), &m); err == nil && m.File != "" {
				matches = append(matches, m)
			}
		}
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	muted := Role(t, tier, theme.RoleFGMuted)

	if len(matches) > 0 {
		fileGroups := make(map[string][]grepJSONMatch)
		var fileOrder []string
		for _, m := range matches {
			if _, exists := fileGroups[m.File]; !exists {
				fileOrder = append(fileOrder, m.File)
			}
			fileGroups[m.File] = append(fileGroups[m.File], m)
		}

		var out []string
		for _, f := range fileOrder {
			group := fileGroups[f]
			out = append(out, accent.Render("• "+f)+" "+subtle.Render(fmt.Sprintf("(%d matches)", len(group))))
			for _, m := range group {
				content := strings.TrimSpace(m.LineContent)
				if ansi.StringWidth(content) > width-8 && width > 16 {
					content = ansi.Truncate(content, width-8, "…")
				}
				numStr := fmt.Sprintf("L%-4d", m.LineNumber)
				out = append(out, "  "+muted.Render(numStr)+" "+content)
			}
		}
		summary := fmt.Sprintf("%d matches in %d files", len(matches), len(fileOrder))
		return summary, out
	}

	return "", lines
}

// FormatFileReadOutput previews file content with line numbers when long.
func FormatFileReadOutput(t theme.Theme, tier theme.Tier, output string, width int) ([]string, bool) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= 6 {
		return lines, false
	}

	subtle := Role(t, tier, theme.RoleFGSubtle)
	muted := Role(t, tier, theme.RoleFGMuted)

	var out []string
	for i := 0; i < min(4, len(lines)); i++ {
		num := muted.Render(fmt.Sprintf("%3d │ ", i+1))
		out = append(out, num+lines[i])
	}
	if len(lines) > 4 {
		out = append(out, subtle.Render(fmt.Sprintf("    │ ... %d more lines ...", len(lines)-4)))
	}
	return out, true
}

// FormatJSONOutput formats JSON objects into readable key-value summary lines.
func FormatJSONOutput(t theme.Theme, tier theme.Tier, output string, width int) []string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &obj); err == nil {
		accent := Role(t, tier, theme.RoleAccent)
		subtle := Role(t, tier, theme.RoleFGSubtle)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var out []string
		for _, k := range keys {
			v := obj[k]
			valStr := fmt.Sprintf("%v", v)
			if ansi.StringWidth(valStr) > 40 {
				valStr = ansi.Truncate(valStr, 40, "…")
			}
			out = append(out, accent.Render(k)+": "+subtle.Render(valStr))
		}
		if len(out) > 0 {
			return out
		}
	}
	return strings.Split(strings.TrimRight(output, "\n"), "\n")
}
