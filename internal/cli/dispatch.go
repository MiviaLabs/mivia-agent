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
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
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
		ResourceKey: toolDispatchTasks,
		Timeout:     time.Duration(dispatchOrchestrationSec(t.cfg.DefaultTimeout, args)) * time.Second,
	}
}

// dispatchOrchestrationSlackSec is the headroom the whole-call budget gets over
// the longest task in the batch, so the call outlives the work it is waiting on.
const dispatchOrchestrationSlackSec = 15

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
	// Headroom over the longest single task. Without it the whole-call budget and
	// each task's own budget are the same number, and the agent loop arms the
	// call's clock before the pool arms the task's — so the outer deadline always
	// fired first, Join returned ctx.Err() with no result, and a batch reported a
	// bare error instead of the per-task results it was about to produce.
	return config.EffectiveTimeoutSec(defaultTimeout, overrides...) + dispatchOrchestrationSlackSec
}
func (t *dispatchTasksTool) Name() string { return toolDispatchTasks }
func (t *dispatchTasksTool) Privileged()  {}
func (t *dispatchTasksTool) Description() string {
	desc := "Execute multiple sub-tasks in PARALLEL. Use this for ALL research, code reviews, " +
		"bug audits, and any work that can be split — never do N sequential passes. " +
		"Each task is a natural language prompt. " +
		"Tasks without dependencies (depends_on) run concurrently. " +
		"Always set handler:\"multi_step\" for tool-using agents. " +
		"Every task always reports its own result and status, so one failure never " +
		"costs you the others. " +
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
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}

	injectHandlerEnum(result, "handler", t.skillReg)
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
		TimeoutSeconds int `json:"timeout_seconds,omitempty"`
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

	_, runResult, err := runThroughCoordinator(ctx, t.dispatcher, t.cfg, tasks, "", t.repo)
	var results []subagents.Result
	if runResult != nil {
		results = runResult.Results
	}
	if runResult != nil && runResult.Err != nil {
		// Return whatever ran, always. Each result carries its own status, so a
		// failed or blocked task is already visible per task, and run_error/status
		// below explain the run itself. finalizeDAG emits one result per task
		// (filling "missing" when absent), so this branch is the one always taken
		// and it is identical to the success branch below — kept separate only so
		// the empty-results fallback underneath stays reachable.
		if len(results) > 0 {
			return t.encodeResults(snapshotTasks(runResult), results), nil
		}
		// No error_ref: a run-level failure (spawn, validation, join, timeout) is
		// never a task's recorded error, so nothing was ever stored under its
		// digest. The full text is inline in "error" instead.
		payload, _ := json.Marshal(map[string]string{
			"error":  runResult.Err.Error(),
			"status": statusFromErr(runResult.Err),
		})
		return string(payload), nil
	}
	// Always return a model-visible body. Transport errors would be wiped to
	// a bare "error: …" string by the agent loop when the body is empty.
	if len(results) > 0 {
		return t.encodeResults(snapshotTasks(runResult), results), nil
	}
	if err != nil {
		// Same reasoning as above: this is a run-level error with no stored
		// content, so it is reported inline only.
		payload, _ := json.Marshal(map[string]string{
			"error":  err.Error(),
			"status": statusFromErr(err),
		})
		return string(payload), nil
	}
	return `{"tasks":[]}`, nil
}

func statusFromErr(err error) string {
	if err == nil {
		return string(ledger.TaskStatusFailed)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(ledger.TaskStatusTimedOut)
	}
	if errors.Is(err, context.Canceled) {
		return string(ledger.TaskStatusCanceled)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "deadline exceeded"):
		return string(ledger.TaskStatusTimedOut)
	case strings.Contains(msg, "canceled"), strings.Contains(msg, "cancelled"):
		return string(ledger.TaskStatusCanceled)
	default:
		return string(ledger.TaskStatusFailed)
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
			handler = handlerMultiStep
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

func (t *dispatchTasksTool) encodeResults(tasks []ledger.TaskSnapshot, results []subagents.Result) string {
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
			out[i].Status = string(ledger.TaskStatusCompleted)
		}
		outputRef, errorRef := storedResultRefs(tasks, r)
		if r.Err != nil {
			out[i].ErrorRef = errorRef
			out[i].Error = r.Err.Error()
			if len(r.Output) > 0 {
				out[i].Output = modelVisibleOutput(r.Output)
				out[i].OutputRef = outputRef
			}
			if out[i].Status == "" {
				out[i].Status = string(ledger.TaskStatusFailed)
			}
		} else if len(r.Output) > 0 {
			out[i].Output = modelVisibleOutput(r.Output)
			out[i].OutputRef = outputRef
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

// snapshotTasks returns a run's task records, or nil when no snapshot is
// available. encodeResults reads recorded references from these, so that a
// reference whose content write failed is reported as absent rather than
// re-minted from memory (INV-AG-10).
func snapshotTasks(result *coordinator.RunResult) []ledger.TaskSnapshot {
	if result == nil {
		return nil
	}
	return result.Snapshot.Tasks
}
