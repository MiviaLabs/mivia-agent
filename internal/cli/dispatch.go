package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// dispatchTasksTool implements tools.Tool by routing multiple tasks through
// the subagents.Pool with optional dependency ordering. Tasks without
// dependencies run concurrently; dependent tasks wait for prerequisites.
type dispatchTasksTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	skillReg   *skills.Registry
	nextBatch  atomic.Uint64
}

func (t *dispatchTasksTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, ResourceKey: "dispatch_tasks"}
}
func (t *dispatchTasksTool) Name() string { return "dispatch_tasks" }
func (t *dispatchTasksTool) Description() string {
	return "Execute multiple sub-tasks in parallel through registered subagent or skill handlers. " +
		"Each task is a natural language prompt. " +
		"Tasks without dependencies (depends_on) run concurrently. " +
		"Use when you need independent analyses that benefit from parallel execution. " +
		"Recommended: 2-4 tasks at once."
}
func (t *dispatchTasksTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Unique task ID (e.g. 't1', 'research_auth')",
						},
						"prompt": map[string]any{
							"type":        "string",
							"description": "Natural language task description for the sub-agent",
						},
						"depends_on": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
							"description": "Task IDs that must complete first",
						},
						"handler": map[string]any{
							"type":        "string",
							"description": "Registered subagent or skill handler; defaults to oneshot",
						},
					},
					"required": []string{"id", "prompt"},
				},
				"description": "Array of 1-16 tasks. Tasks without depends_on run concurrently.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Override default timeout per task (default 600). Set higher for complex multi-step tasks.",
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}
}
func (t *dispatchTasksTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Tasks []struct {
			ID        string   `json:"id"`
			Prompt    string   `json:"prompt"`
			DependsOn []string `json:"depends_on,omitempty"`
			Handler   string   `json:"handler,omitempty"`
		} `json:"tasks"`
		TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("dispatch_tasks: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"tasks":[]}`, nil
	}

	timeout := t.cfg.DefaultTimeout
	if params.TimeoutSeconds > 0 {
		timeout = params.TimeoutSeconds
	}

	pool := subagents.New(t.dispatcher, subagents.Policy{
		Workers:   t.cfg.MaxWorkers,
		MaxDepth:  t.cfg.MaxDepth,
		MaxFanout: t.cfg.MaxFanout,
		Timeout:   time.Duration(timeout) * time.Second,
		Partial:   t.cfg.PartialResults,
	})

	tasks := t.buildTasks(params.Tasks, timeout)

	results, err := pool.Run(ctx, tasks)
	if err != nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(payload), err
	}

	return t.encodeResults(results), nil
}

func (t *dispatchTasksTool) buildTasks(params []struct {
	ID        string   `json:"id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"`
	Handler   string   `json:"handler,omitempty"`
}, timeoutSeconds int) []subagents.Task {
	tasks := make([]subagents.Task, len(params))
	batchID := fmt.Sprintf("dispatch:%d", t.nextBatch.Add(1))
	for i, pt := range params {
		handler := pt.Handler
		if handler == "" {
			// NOTE(mivia): default is "oneshot" (no tools, single LLM call).
			// Change to "multi_step" to give tasks tool access by default.
			handler = "oneshot"
		}
		permission := ""
		if t.skillReg != nil {
			if skill, ok := t.skillReg.Get(handler); ok {
				permission = skill.Permission
			}
		}
		input, _ := json.Marshal(pt.Prompt)
		tasks[i] = subagents.Task{ID: pt.ID, InvocationKey: batchID + ":" + pt.ID, Name: handler, Permission: permission, Owner: "mivia", Input: input, DependsOn: pt.DependsOn, Timeout: time.Duration(timeoutSeconds) * time.Second}
	}
	return tasks
}

func (t *dispatchTasksTool) encodeResults(results []subagents.Result) string {
	type taskResult struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Output string `json:"output,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	out := make([]taskResult, len(results))
	for i, r := range results {
		out[i].TaskID = r.TaskID
		out[i].Status = r.Status
		if r.Status == "" {
			out[i].Status = "completed"
		}
		if r.Err != nil {
			out[i].Error = r.Err.Error()
			if out[i].Status == "" {
				out[i].Status = "failed"
			}
		} else if len(r.Output) > 0 {
			// Try to extract the "output" field from the JSON result
			var parsed map[string]any
			if err := json.Unmarshal(r.Output, &parsed); err == nil {
				if s, ok := parsed["output"].(string); ok {
					out[i].Output = s
				}
			}
			if out[i].Output == "" {
				out[i].Output = string(r.Output)
			}
		}
	}
	outJSON, _ := json.Marshal(out)
	return string(outJSON)
}

// Ensure dispatchTasksTool implements required interfaces at compile time.
var _ tools.Tool = (*dispatchTasksTool)(nil)
var _ tools.CapableTool = (*dispatchTasksTool)(nil)
