package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Framing for model-visible dynamic system reminders.
//
// Dynamic system reminders provide high-priority operational directives
// placed at the recency peak of the context window (appended to recent tool
// observations or user turns) without mutating the root system prompt at
// token index 0. This preserves prefix caching while mitigating U-shaped
// attention decay and task drift over long trajectories.
const (
	systemReminderOpenTag  = "<system-reminder>"
	systemReminderCloseTag = "</system-reminder>"

	// neutralizedReminderTag replaces forged reminder tags emitted by tools or logs.
	neutralizedReminderTag = "[escaped-reminder-tag]"

	// MaxSystemReminderBytes bounds the maximum size of an injected reminder.
	MaxSystemReminderBytes = 2048
)

// forgedReminderTag matches anything a model could interpret as an opening or
// closing system-reminder delimiter tag.
var forgedReminderTag = regexp.MustCompile(`(?i)<\s*/?\s*system-reminder\b[^>]{0,512}>`)

// FrameSystemReminder wraps reminder text into a delimited, tag-neutralized
// block. Blank input returns an empty string.
func FrameSystemReminder(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > MaxSystemReminderBytes {
		trimmed = trimmed[:MaxSystemReminderBytes]
	}
	neutralized := forgedReminderTag.ReplaceAllString(trimmed, neutralizedReminderTag)
	return fmt.Sprintf("%s\n%s\n%s", systemReminderOpenTag, neutralized, systemReminderCloseTag)
}

// AppendSystemReminder appends a framed system reminder to a tool observation
// or turn body.
func AppendSystemReminder(body, reminder string) string {
	framed := FrameSystemReminder(reminder)
	switch {
	case framed == "":
		return body
	case strings.TrimSpace(body) == "":
		return framed
	default:
		return body + "\n\n" + framed
	}
}

// LoopBreakerReminder generates an anti-loop reactive steering directive when
// repeated tool failures occur consecutively in an execution trajectory.
func LoopBreakerReminder(consecutiveFailures int) string {
	if consecutiveFailures < 3 {
		return ""
	}
	return "Notice: 3 consecutive tool failures detected. Pause and re-evaluate the hypothesis before you run another tool call. Re-read the task description and state a simpler alternative hypothesis."
}
