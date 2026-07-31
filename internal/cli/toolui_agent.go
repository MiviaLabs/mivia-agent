package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// summarizeAgentTool builds operator-facing one-liners for delegation tools.
func summarizeAgentTool(name, detail, result string) string {
	switch name {
	case handlerDelegate:
		return summarizeDelegate(detail, result)
	case toolDispatchTasks:
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
	return boundedToolText(s, max)
}

func expandSectionLabel(name string, input bool) string {
	if !input {
		return "output"
	}
	switch name {
	case handlerDelegate:
		return "task"
	case toolDispatchTasks:
		return "tasks"
	default:
		return "input"
	}
}
