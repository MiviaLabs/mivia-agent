package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// isMessagingTool reports whether the tool is an agent-messaging or blackboard tool.
func isMessagingTool(name string) bool {
	switch strings.ToLower(name) {
	case "post_message", "send_to_task", "run_messages", "send_message":
		return true
	default:
		return false
	}
}

// FormatMessagingOutput formats inter-agent messages and blackboard entries into structured cards.
func FormatMessagingOutput(t theme.Theme, tier theme.Tier, name string, args map[string]any, output string, width int) (summary string, body []string, collapsible bool) {
	switch strings.ToLower(name) {
	case "post_message":
		return formatPostMessage(t, tier, args, output, width)
	case "send_to_task":
		return formatSendToTask(t, tier, args, output)
	case "send_message":
		return formatSendMessage(t, tier, args, output)
	case "run_messages":
		return formatRunMessages(t, tier, output, width)
	default:
		return "", strings.Split(output, "\n"), false
	}
}

func formatPostMessage(t theme.Theme, tier theme.Tier, args map[string]any, output string, width int) (string, []string, bool) {
	kind := strings.ToLower(getString(args, "kind"))
	msgBody := getString(args, "body")
	if msgBody == "" {
		msgBody = output
	}
	toRole := getString(args, "to_role")
	inReplyTo := getString(args, "in_reply_to")

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)
	warning := Role(t, tier, theme.RoleWarning)
	success := Role(t, tier, theme.RoleSuccess)
	muted := Role(t, tier, theme.RoleFGMuted)

	switch kind {
	case "finding":
		var lines []string
		lines = append(lines, warning.Bold(true).Render("📌 Blackboard Finding"))
		if msgBody != "" {
			lines = append(lines, "  "+fg.Render(msgBody))
		}
		if refs, ok := args["refs"].([]any); ok && len(refs) > 0 {
			var refStrs []string
			for _, r := range refs {
				refStrs = append(refStrs, fmt.Sprint(r))
			}
			lines = append(lines, subtle.Render("  refs: ")+muted.Render(strings.Join(refStrs, ", ")))
		}
		return "[Blackboard Finding]", lines, false

	case "question":
		var lines []string
		lines = append(lines, warning.Bold(true).Render("❓ Question (Blocking)"))
		if msgBody != "" {
			lines = append(lines, "  "+fg.Render(msgBody))
		}
		if waitSec, ok := args["wait_seconds"].(float64); ok && waitSec > 0 {
			lines = append(lines, subtle.Render(fmt.Sprintf("  ⏳ waiting up to %.0fs for parent response", waitSec)))
		}
		return "[Question to Orchestrator]", lines, false

	case "ask":
		var lines []string
		lines = append(lines, accent.Bold(true).Render(fmt.Sprintf("🤝 Peer Referral: ask → @%s", toRole)))
		if msgBody != "" {
			lines = append(lines, "  "+fg.Render(msgBody))
		}
		return fmt.Sprintf("[Ask → @%s]", toRole), lines, false

	case "answer":
		header := "💡 Answer Reply"
		if inReplyTo != "" {
			header += fmt.Sprintf(" (in reply to %s)", inReplyTo)
		}
		var lines []string
		lines = append(lines, success.Bold(true).Render(header))
		if msgBody != "" {
			lines = append(lines, "  "+fg.Render(msgBody))
		}
		return "[Answer Reply]", lines, false

	default:
		return formatGenericMessage(t, tier, "Message", msgBody, width)
	}
}

func formatSendToTask(t theme.Theme, tier theme.Tier, args map[string]any, output string) (string, []string, bool) {
	action := strings.ToLower(getString(args, "action"))
	taskID := getString(args, "task_id")
	msgBody := getString(args, "body")
	if msgBody == "" {
		msgBody = output
	}

	accent := Role(t, tier, theme.RoleAccent)
	success := Role(t, tier, theme.RoleSuccess)
	fg := Role(t, tier, theme.RoleFG)

	if action == "answer" {
		var lines []string
		lines = append(lines, success.Bold(true).Render(fmt.Sprintf("💡 Answer → %s", taskID)))
		if msgBody != "" {
			lines = append(lines, "  "+fg.Render(msgBody))
		}
		return fmt.Sprintf("[Answer → %s]", taskID), lines, false
	}

	var lines []string
	lines = append(lines, accent.Bold(true).Render(fmt.Sprintf("✉ Steer → %s", taskID)))
	if msgBody != "" {
		lines = append(lines, "  "+fg.Render(msgBody))
	}
	return fmt.Sprintf("[Steer → %s]", taskID), lines, false
}

func formatSendMessage(t theme.Theme, tier theme.Tier, args map[string]any, output string) (string, []string, bool) {
	recipient := getString(args, "Recipient")
	if recipient == "" {
		recipient = getString(args, "recipient")
	}
	msg := getString(args, "Message")
	if msg == "" {
		msg = getString(args, "message")
	}
	if msg == "" {
		msg = output
	}

	accent := Role(t, tier, theme.RoleAccent)
	fg := Role(t, tier, theme.RoleFG)

	var lines []string
	lines = append(lines, accent.Bold(true).Render(fmt.Sprintf("✉ Message → %s", recipient)))
	if msg != "" {
		lines = append(lines, "  "+fg.Render(msg))
	}
	return fmt.Sprintf("[Message → %s]", recipient), lines, false
}

func formatRunMessages(t theme.Theme, tier theme.Tier, output string, width int) (string, []string, bool) {
	subtle := Role(t, tier, theme.RoleFGSubtle)
	var lines []string
	lines = append(lines, subtle.Render("📋 Run Blackboard:"))

	var list []map[string]any
	if err := json.Unmarshal([]byte(output), &list); err == nil && len(list) > 0 {
		for i, m := range list {
			kind := fmt.Sprint(m["kind"])
			body := fmt.Sprint(m["body"])
			from := fmt.Sprint(m["from"])
			lines = append(lines, fmt.Sprintf("  %d. [%s] %s: %s", i+1, kind, from, body))
		}
		return "[Blackboard Messages]", lines, len(lines) > 6
	}
	for _, ln := range strings.Split(output, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, "  "+ansi.Truncate(ln, width-4, "…"))
		}
	}
	return "[Blackboard Messages]", lines, len(lines) > 6
}

func formatGenericMessage(t theme.Theme, tier theme.Tier, title, body string, width int) (string, []string, bool) {
	accent := Role(t, tier, theme.RoleAccent)
	fg := Role(t, tier, theme.RoleFG)
	var lines []string
	lines = append(lines, accent.Bold(true).Render("✉ "+title))
	for _, ln := range strings.Split(body, "\n") {
		lines = append(lines, "  "+fg.Render(ansi.Truncate(ln, width-4, "…")))
	}
	return "[" + title + "]", lines, len(lines) > 6
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}
