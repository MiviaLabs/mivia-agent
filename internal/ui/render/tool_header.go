package render

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// FormatToolDetail renders a domain-specific, readable detail string for a tool call.
// It formats commands with "$ <cmd>", file tools with their relative path, search tools with
// their query/scope, and web tools with their domain, falling back to FormatArgs for generic tools.
func FormatToolDetail(name string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	lower := strings.ToLower(name)

	if detail := formatCommandDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatLedgerDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatSkillResourceDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatMemoryDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatWorkflowDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatFileDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatSearchDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatWebDetail(lower, args); detail != "" {
		return detail
	}
	if detail := formatMessagingDetail(lower, args); detail != "" {
		return detail
	}
	return FormatArgs(args)
}

func formatMessagingDetail(lower string, args map[string]any) string {
	switch lower {
	case "post_message":
		kind := getString(args, "kind")
		toRole := getString(args, "to_role")
		if kind == "ask" && toRole != "" {
			return "ask → @" + toRole
		}
		if kind != "" {
			return "kind=" + kind
		}
	case "send_to_task":
		action := getString(args, "action")
		taskID := getString(args, "task_id")
		if action != "" && taskID != "" {
			return action + " → " + taskID
		}
	case "send_message":
		recipient := getString(args, "Recipient")
		if recipient == "" {
			recipient = getString(args, "recipient")
		}
		if recipient != "" {
			return "→ @" + recipient
		}
	}
	return ""
}

func formatLedgerDetail(lower string, args map[string]any) string {
	if !strings.Contains(lower, "ledger") && lower != "read_output" {
		return ""
	}
	if ref, ok := args["ref"].(string); ok && ref != "" {
		short := ref
		parts := strings.Split(ref, ":")
		if len(parts) >= 3 && len(parts[2]) > 8 {
			short = fmt.Sprintf("%s:%s:%s", parts[0], parts[1], parts[2][:8])
		}
		if offset, ok := args["offset"]; ok {
			if offNum, ok := offset.(float64); ok && offNum > 0 {
				return fmt.Sprintf("%s [offset %d]", short, int(offNum))
			}
		}
		return short
	}
	return ""
}

func formatSkillResourceDetail(lower string, args map[string]any) string {
	if lower != "read_skill_resource" && lower != "skill_resource" {
		return ""
	}
	skill, _ := args["skill"].(string)
	res, _ := args["resource"].(string)
	if skill != "" && res != "" {
		return skill + "/" + res
	}
	return ""
}

func formatWorkflowDetail(lower string, args map[string]any) string {
	if !strings.Contains(lower, "workflow") {
		return ""
	}
	wf, _ := args["workflow"].(string)
	runID, _ := args["run_id"].(string)
	if wf != "" && runID != "" {
		return fmt.Sprintf("%q (%s)", wf, runID)
	}
	if wf != "" {
		return fmt.Sprintf("%q", wf)
	}
	if runID != "" {
		return runID
	}
	return ""
}

func formatMemoryDetail(lower string, args map[string]any) string {
	if !strings.Contains(lower, "memory") {
		return ""
	}
	var text string
	for _, k := range []string{"query", "summary", "key", "id"} {
		if v, ok := args[k].(string); ok && v != "" {
			text = fmt.Sprintf("%q", v)
			break
		}
	}
	scope, _ := args["scope"].(string)
	if scope != "" {
		if text != "" {
			return text + " [" + scope + "]"
		}
		return "[" + scope + "]"
	}
	return text
}

func formatCommandDetail(lower string, args map[string]any) string {
	if lower == "run_command" || lower == "bash" || lower == "terminal" || lower == "exec" || lower == "command" {
		for _, k := range []string{"command", "CommandLine", "cmd"} {
			if v, ok := args[k].(string); ok && v != "" {
				return "$ " + v
			}
		}
	}
	return ""
}

func formatFileDetail(lower string, args map[string]any) string {
	isFileTool := strings.Contains(lower, "file") ||
		strings.Contains(lower, "edit") ||
		strings.Contains(lower, "replace") ||
		strings.Contains(lower, "patch") ||
		strings.Contains(lower, "dir") ||
		lower == "view_file" || lower == "write_to_file"

	if !isFileTool {
		return ""
	}

	for _, k := range []string{"file_path", "path", "AbsolutePath", "TargetFile", "target_file", "filePath", "filename", "DirectoryPath", "SearchDirectory"} {
		if v, ok := args[k].(string); ok && v != "" {
			if start, ok := args["StartLine"]; ok {
				if end, ok2 := args["EndLine"]; ok2 {
					return fmt.Sprintf("%s [L%v-L%v]", v, start, end)
				}
				return fmt.Sprintf("%s [L%v]", v, start)
			}
			return v
		}
	}
	return ""
}

func formatSearchDetail(lower string, args map[string]any) string {
	isSearch := strings.Contains(lower, "grep") ||
		strings.Contains(lower, "glob") ||
		strings.Contains(lower, "find") ||
		strings.Contains(lower, "symbol") ||
		strings.Contains(lower, "reference")

	if !isSearch {
		return ""
	}

	var pattern string
	for _, k := range []string{"pattern", "query", "Query", "symbol"} {
		if v, ok := args[k].(string); ok && v != "" {
			pattern = v
			break
		}
	}
	if pattern == "" {
		return ""
	}

	for _, k := range []string{"path", "SearchPath", "file_path"} {
		if v, ok := args[k].(string); ok && v != "" {
			return fmt.Sprintf("%q in %s", pattern, v)
		}
	}
	return fmt.Sprintf("%q", pattern)
}

func formatWebDetail(lower string, args map[string]any) string {
	isWeb := strings.Contains(lower, "url") ||
		strings.Contains(lower, "fetch") ||
		strings.Contains(lower, "web") ||
		strings.Contains(lower, "extract") ||
		lower == "search" || lower == "search_web"

	if !isWeb {
		return ""
	}

	for _, k := range []string{"url", "Url", "URL"} {
		if rawURL, ok := args[k].(string); ok && rawURL != "" {
			if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
				path := u.Path
				if ansi.StringWidth(path) > 30 {
					path = ansi.Truncate(path, 30, "…")
				}
				return fmt.Sprintf("[%s] %s", u.Host, path)
			}
			return rawURL
		}
	}

	for _, k := range []string{"query", "Query"} {
		if q, ok := args[k].(string); ok && q != "" {
			return fmt.Sprintf("%q", q)
		}
	}
	return ""
}
