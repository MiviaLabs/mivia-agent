package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// orchestrationRunEnvelope mirrors the common result shape shared by
// inspect_agents, spawn_agent, and join_run (internal/cliorchestrate).
// inspect_agents populates Tasks+Parks; spawn_agent/join_run populate
// Tasks+TaskResults and an optional RunError. Kept narrow and independent
// of that package - the UI layer must not import orchestration packages
// (mivia-ui isolation, INV-TUI-29).
type orchestrationRunEnvelope struct {
	RunID       string                    `json:"run_id"`
	DisplayName string                    `json:"display_name"`
	Status      string                    `json:"status"`
	RunError    string                    `json:"run_error"`
	Tasks       []orchestrationTaskInfo   `json:"tasks"`
	TaskResults []dispatchTaskResultView  `json:"task_results"`
	Parks       []orchestrationParkedItem `json:"parks"`
}

type orchestrationTaskInfo struct {
	TaskID      string   `json:"task_id"`
	DisplayName string   `json:"display_name"`
	Status      string   `json:"status"`
	DependsOn   []string `json:"depends_on"`
	OutputRef   string   `json:"output_ref"`
	ErrorRef    string   `json:"error_ref"`
}

// orchestrationParkedItem only needs enough of coordinator.ParkedQuestion
// to render a one-line summary; unrecognized fields are ignored.
type orchestrationParkedItem struct {
	TaskID   string `json:"task_id"`
	Question string `json:"question"`
}

// orchestrationErrorEnvelope matches the flat {"error":"..."} shapes
// spawn_agent/join_run/inspect_agents/list_run_events all return for
// input/access failures (e.g. "unknown run_id", "at least one task is
// required", "wait must be one of none, task, or run").
type orchestrationErrorEnvelope struct {
	Error string `json:"error"`
}

// maxOrchestrationTaskRows caps how many task lines FormatOrchestrationRunOutput
// renders before collapsing the rest, matching FormatDispatchTasksOutput's
// truncate-with-notice idiom.
const maxOrchestrationTaskRows = 8

// FormatOrchestrationRunOutput formats an inspect_agents/spawn_agent/
// join_run result into a run summary, a per-task status list, and any
// pending parked questions, instead of a raw JSON dump.
func FormatOrchestrationRunOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := strings.TrimSpace(output)
	danger := Role(t, tier, theme.RoleDanger)
	warn := Role(t, tier, theme.RoleWarning)
	subtle := Role(t, tier, theme.RoleFGSubtle)

	var errEnv orchestrationErrorEnvelope
	if json.Unmarshal([]byte(trimmed), &errEnv) == nil && errEnv.Error != "" {
		return "✖ " + errEnv.Error, nil
	}

	var env orchestrationRunEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil || env.RunID == "" {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	summary := env.DisplayName
	if summary == "" {
		summary = shortenRef(env.RunID)
	}
	if env.Status != "" {
		summary += " · " + env.Status
	}

	var out []string
	if env.RunError != "" {
		out = append(out, danger.Render("✖ ")+env.RunError)
	}

	if len(env.TaskResults) > 0 {
		out = append(out, renderTaskResultRows(t, tier, env.TaskResults, width, maxOrchestrationTaskRows, "more tasks")...)
	} else if len(env.Tasks) > 0 {
		out = append(out, renderOrchestrationTaskInfoRows(t, tier, env.Tasks, width)...)
	}

	if len(env.Parks) > 0 {
		out = append(out, warn.Render(fmt.Sprintf("⚠ %d task(s) waiting on an answer", len(env.Parks))))
		for _, p := range env.Parks {
			line := subtle.Render("  " + p.TaskID)
			if p.Question != "" {
				line += "  " + p.Question
			}
			out = append(out, line)
		}
	}

	if len(out) == 0 {
		out = []string{subtle.Render("no tasks")}
	}
	return summary, out
}

// renderOrchestrationTaskInfoRows renders inspect_agents' bare task list
// (no synopsis/elapsed - just id/status/refs), mapping each task's
// dispatchTaskResultView-compatible fields and delegating to the shared
// row renderer.
func renderOrchestrationTaskInfoRows(t theme.Theme, tier theme.Tier, tasks []orchestrationTaskInfo, width int) []string {
	views := make([]dispatchTaskResultView, len(tasks))
	for i, task := range tasks {
		views[i] = dispatchTaskResultView{
			TaskID:    task.TaskID,
			Status:    task.Status,
			OutputRef: task.OutputRef,
			ErrorRef:  task.ErrorRef,
		}
	}
	return renderTaskResultRows(t, tier, views, width, maxOrchestrationTaskRows, "more tasks")
}
