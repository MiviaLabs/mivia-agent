package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
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
	repo       ledger.LedgerRepository
	skillReg   *skills.Registry
	nextBatch  atomic.Uint64
}

func (t *dispatchTasksTool) Capability(args json.RawMessage) tools.Capability {
	// Capability.Timeout is the parent agent-loop budget for this tool call.
	// It may exceed the default 60s ToolTimeout so multi-step batches are not
	// killed early; EffectiveTimeoutSec still keeps a finite safety ceiling.
	return tools.Capability{
		Class:       tools.ExecutionExternal,
		ResourceKey: "dispatch_tasks",
		Timeout:     time.Duration(dispatchOrchestrationSec(t.cfg.DefaultTimeout, args)) * time.Second,
	}
}

// dispatchOrchestrationSec picks the wall-clock budget for the whole
// dispatch_tasks invocation from config, batch timeout_seconds, and any
// per-task timeout_seconds (max wins). Always positive.
func dispatchOrchestrationSec(defaultTimeout int, args json.RawMessage) int {
	var params struct {
		TimeoutSeconds int `json:"timeout_seconds"`
		Tasks          []struct {
			TimeoutSeconds int `json:"timeout_seconds"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(args, &params)
	overrides := make([]int, 0, 1+len(params.Tasks))
	overrides = append(overrides, params.TimeoutSeconds)
	for _, task := range params.Tasks {
		overrides = append(overrides, task.TimeoutSeconds)
	}
	return config.EffectiveTimeoutSec(defaultTimeout, overrides...)
}
func (t *dispatchTasksTool) Name() string { return "dispatch_tasks" }
func (t *dispatchTasksTool) Privileged()  {}
func (t *dispatchTasksTool) Description() string {
	desc := "Execute multiple sub-tasks in PARALLEL. Use this for ALL research, code reviews, " +
		"bug audits, and any work that can be split — never do N sequential passes. " +
		"Each task is a natural language prompt. " +
		"Tasks without dependencies (depends_on) run concurrently. " +
		"Always set handler:\"multi_step\" for tool-using agents and partial_results: true " +
		"for audit/challenge rounds (so one failure does not lose all results). " +
		"Recommended: 2-4 tasks at once. " +
		"If dispatch_tasks fails, retry with fewer tasks or switch to spawn_agent. " +
		"Use timeout_seconds to set a per-batch budget (0 uses config default or a finite safety ceiling). " +
		"Results include each task's structured output, correlation reference, status (completed/failed/timed_out/canceled), elapsed, steps, and step_count. " +
		"Heartbeat/progress events appear in the UI during long-running tasks."
	if t.skillReg != nil {
		if infos := t.skillReg.ListModelFacing(nil); len(infos) > 0 {
			displays := make([]string, len(infos))
			for i, info := range infos {
				displays[i] = info.Display
			}
			desc += " Available skill handlers: " + strings.Join(displays, ", ") + "."
		}
	}
	return desc
}
func (t *dispatchTasksTool) Parameters() map[string]any {
	result := map[string]any{
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
							"description": "Registered subagent or skill handler; defaults to multi_step (tools enabled)",
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Override timeout for this task (seconds). 0 uses batch/config default (finite safety ceiling applies).",
						},
					},
					"required": []string{"id", "prompt"},
				},
				"description": "Array of 1-16 tasks. Tasks without depends_on run concurrently.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Per-task timeout budget in seconds. 0 uses config default; runtime always applies a finite safety ceiling so batches cannot hang forever. Raise for long multi-step work.",
			},
			"partial_results": map[string]any{
				"type":        "boolean",
				"description": "Return partial results if some tasks fail instead of failing the whole batch (default false)",
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}

	// Build enum list: built-in handlers + registered skill names.
	enumValues := []string{"multi_step", "delegate", "oneshot"}
	if t.skillReg != nil {
		for _, info := range t.skillReg.ListModelFacing(nil) {
			enumValues = append(enumValues, info.Name)
		}
	}

	// Navigate to the handler property map and inject the enum.
	props := result["properties"].(map[string]any)
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	handler := itemProps["handler"].(map[string]any)
	handler["enum"] = enumValues

	return result
}
func (t *dispatchTasksTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Tasks []struct {
			ID             string   `json:"id"`
			Prompt         string   `json:"prompt"`
			DependsOn      []string `json:"depends_on,omitempty"`
			Handler        string   `json:"handler,omitempty"`
			TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
		} `json:"tasks"`
		TimeoutSeconds int  `json:"timeout_seconds,omitempty"`
		PartialResults bool `json:"partial_results,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("dispatch_tasks: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"tasks":[]}`, nil
	}

	// Always resolve a positive batch timeout so multi_step / pool work is
	// bounded even when default_timeout_seconds is 0.
	batchTimeout := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, params.TimeoutSeconds)

	tasks := t.buildTasks(params.Tasks, batchTimeout)

	coordCfg := t.cfg
	coordCfg.PartialResults = params.PartialResults
	_, runResult, err := runThroughCoordinator(ctx, t.dispatcher, coordCfg, tasks, "", t.repo)
	var results []subagents.Result
	if runResult != nil {
		results = runResult.Results
	}
	if runResult != nil && runResult.Err != nil {
		// K1: When there are partial results despite an error (e.g. timeout),
		// return the completed results with a partial status marker so the
		// caller does not lose work that already finished on disk.
		if len(results) > 0 {
			return t.encodeResults(results), nil
		}
		payload, _ := json.Marshal(map[string]string{
			"error_ref": orchestrationReference("error", []byte(runResult.Err.Error())),
			"error":     runResult.Err.Error(),
			"status":    statusFromErr(runResult.Err),
		})
		return string(payload), nil
	}
	// Always return a model-visible body. Transport errors would be wiped to
	// a bare "error: …" string by the agent loop when the body is empty.
	if len(results) > 0 {
		return t.encodeResults(results), nil
	}
	if err != nil {
		payload, _ := json.Marshal(map[string]string{
			"error_ref": orchestrationReference("error", []byte(err.Error())),
			"error":     err.Error(),
			"status":    statusFromErr(err),
		})
		return string(payload), nil
	}
	return `{"tasks":[]}`, nil
}

func statusFromErr(err error) string {
	if err == nil {
		return "failed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "deadline exceeded"):
		return "timed_out"
	case strings.Contains(msg, "canceled"), strings.Contains(msg, "cancelled"):
		return "canceled"
	default:
		return "failed"
	}
}

func (t *dispatchTasksTool) buildTasks(params []struct {
	ID             string   `json:"id"`
	Prompt         string   `json:"prompt"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Handler        string   `json:"handler,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}, batchTimeout int) []subagents.Task {
	tasks := make([]subagents.Task, len(params))
	batchID := fmt.Sprintf("dispatch:%d", t.nextBatch.Add(1))
	for i, pt := range params {
		handler := pt.Handler
		if handler == "" {
			handler = "multi_step"
		}
		permission := ""
		if t.skillReg != nil {
			if skill, ok := t.skillReg.Get(handler); ok {
				permission = skill.Permission
			}
		}
		input, _ := json.Marshal(pt.Prompt)
		// Per-task timeout overrides batch timeout.
		taskTimeout := batchTimeout
		if pt.TimeoutSeconds > 0 {
			taskTimeout = pt.TimeoutSeconds
		}
		tasks[i] = subagents.Task{
			ID: pt.ID, InvocationKey: batchID + ":" + pt.ID,
			Name: handler, Permission: permission, Owner: "mivia",
			Input: input, DependsOn: pt.DependsOn,
			Timeout: time.Duration(taskTimeout) * time.Second,
		}
	}
	return tasks
}

func (t *dispatchTasksTool) encodeResults(results []subagents.Result) string {
	type taskResult struct {
		TaskID    string `json:"task_id"`
		Status    string `json:"status"`
		Output    any    `json:"output,omitempty"`
		OutputRef string `json:"output_ref,omitempty"`
		ErrorRef  string `json:"error_ref,omitempty"`
		Error     string `json:"error,omitempty"`
		Steps     int    `json:"steps,omitempty"`
		Elapsed   string `json:"elapsed,omitempty"`
		StepCount int64  `json:"step_count,omitempty"`
	}
	out := make([]taskResult, len(results))
	for i, r := range results {
		out[i].TaskID = r.TaskID
		out[i].Status = r.Status
		if r.Status == "" {
			out[i].Status = "completed"
		}
		if r.Err != nil {
			out[i].ErrorRef = orchestrationReference("error", []byte(r.Err.Error()))
			out[i].Error = r.Err.Error()
			if len(r.Output) > 0 {
				out[i].Output = modelVisibleOutput(r.Output)
				out[i].OutputRef = orchestrationReference("output", r.Output)
			}
			if out[i].Status == "" {
				out[i].Status = "failed"
			}
		} else if len(r.Output) > 0 {
			out[i].Output = modelVisibleOutput(r.Output)
			out[i].OutputRef = orchestrationReference("output", r.Output)
			var parsed map[string]any
			if err := json.Unmarshal(r.Output, &parsed); err == nil {
				if s, ok := parsed["elapsed"].(string); ok {
					out[i].Elapsed = s
				}
				if s, ok := parsed["steps"].(float64); ok {
					out[i].Steps = int(s)
				}
				if s, ok := parsed["step_count"].(float64); ok {
					out[i].StepCount = int64(s)
				}
			}
		}
	}
	outJSON, _ := json.Marshal(out)
	return string(outJSON)
}

// Ensure dispatchTasksTool implements required interfaces at compile time.
var _ tools.Tool = (*dispatchTasksTool)(nil)
var _ tools.CapableTool = (*dispatchTasksTool)(nil)
