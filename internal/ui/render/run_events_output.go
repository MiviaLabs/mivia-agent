package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// runEventsEnvelope mirrors list_run_events' {run_id, events, count,
// truncated} result (internal/clichat/ledger_tools.go's Execute). Kept
// narrow and independent of that package - the UI layer must not import
// the tool packages (mivia-ui isolation, INV-TUI-29).
type runEventsEnvelope struct {
	RunID     string          `json:"run_id"`
	Events    []runEventEntry `json:"events"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated"`
}

type runEventEntry struct {
	ID        string `json:"id"`
	Sequence  uint64 `json:"sequence"`
	Kind      string `json:"kind"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	CreatedAt string `json:"created_at"`
}

// runEventsErrorEnvelope matches list_run_events' unknown-kind rejection
// shape ({"error":"unknown kind","accepted":[...]}) - distinct from the
// bare {"error":"unknown run_id"} access-denied shape shared across the
// whole run-scoped tool family.
type runEventsErrorEnvelope struct {
	Error    string   `json:"error"`
	Accepted []string `json:"accepted"`
}

// maxRunEventRows caps how many event lines FormatRunEventsOutput renders
// before collapsing the rest, matching FormatDispatchTasksOutput's
// truncate-with-notice idiom.
const maxRunEventRows = 12

// FormatRunEventsOutput formats a list_run_events result into a
// chronological, icon-coded event timeline instead of a raw JSON dump.
func FormatRunEventsOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := strings.TrimSpace(output)
	subtle := Role(t, tier, theme.RoleFGSubtle)

	var errEnv runEventsErrorEnvelope
	if json.Unmarshal([]byte(trimmed), &errEnv) == nil && errEnv.Error != "" {
		summary := "✖ " + errEnv.Error
		if len(errEnv.Accepted) == 0 {
			return summary, nil
		}
		return summary, []string{subtle.Render("accepted: " + strings.Join(errEnv.Accepted, ", "))}
	}

	var env runEventsEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil || env.RunID == "" {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	summary := fmt.Sprintf("%d events", env.Count)
	if env.Truncated {
		summary += " · truncated"
	}

	if len(env.Events) == 0 {
		return summary, []string{subtle.Render("no events recorded")}
	}

	rows := env.Events
	var tail string
	if len(rows) > maxRunEventRows {
		tail = fmt.Sprintf("… %d more events", len(rows)-maxRunEventRows)
		rows = rows[:maxRunEventRows]
	}

	fg := Role(t, tier, theme.RoleFG)
	var out []string
	for _, ev := range rows {
		icon, iconRole := runEventKindStyle(t, tier, ev.Kind)
		line := iconRole.Render(icon) + " " + fg.Render(ev.Kind)
		if ev.TaskID != "" {
			line += subtle.Render(" " + ev.TaskID)
		}
		if ev.CreatedAt != "" {
			line += subtle.Render(" " + ev.CreatedAt)
		}
		out = append(out, line)
	}
	if tail != "" {
		out = append(out, subtle.Render(tail))
	}
	return summary, out
}

// runEventKindStyle maps a lifecycle event kind to an icon and role,
// reusing the theme's success/danger/warning roles the way
// FormatDispatchTasksOutput and FormatDiagnosticsOutput already do for
// their own status vocabularies.
func runEventKindStyle(t theme.Theme, tier theme.Tier, kind string) (string, lipgloss.Style) {
	switch kind {
	case "task_completed", "run_created":
		return "✔", Role(t, tier, theme.RoleSuccess)
	case "task_failed", "task_timed_out", "task_canceled", "task_interrupted_unrecoverable":
		return "✖", Role(t, tier, theme.RoleDanger)
	case "task_blocked", "task_cancel_requested", "task_retry_pending", "task_retry_queued":
		return "⚠", Role(t, tier, theme.RoleWarning)
	default:
		return "•", Role(t, tier, theme.RoleFG)
	}
}
