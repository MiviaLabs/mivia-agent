// Package cli provides orchestration tool implementations that bridge the
// model-facing tool set with the coordinator's async run model.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// runThroughCoordinator is the compatibility seam for legacy tools. It keeps
// delegate and dispatch_tasks on the same ledger, pool, cancellation, and
// identity path as the canonical orchestration tools.
func runThroughCoordinator(ctx context.Context, d *runtime.Dispatcher, cfg config.SubagentConfig, tasks []subagents.Task, key string, repos ...ledger.LedgerRepository) (ledger.RunSnapshot, *coordinator.RunResult, error) {
	c := initCoordinator(d, cfg, repos...)
	handle, err := c.Spawn(ctx, tasks, key, cfg.PartialResults)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	// Store the orchestration handle so that runs created by delegate and
	// dispatch_tasks are visible to join_run, cancel_run, and inspect_agents.
	snap, err := c.Inspect(ctx, handle)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	repo := effectiveOrchestrationRepo(defaultOrchestrationRepo)
	if len(repos) > 0 {
		repo = effectiveOrchestrationRepo(repos[0])
	}
	storeOrchestrationHandle(snap.RunID, &orchestrationHandle{
		coord: c, handle: handle, repo: repo, dispatcher: d,
	})
	result, err := c.Join(ctx, handle)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	return result.Snapshot, result, nil
}

// ---------------------------------------------------------------------------
// spawn_agent
// ---------------------------------------------------------------------------

type spawnAgentTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *spawnAgentTool) Name() string { return "spawn_agent" }
func (t *spawnAgentTool) Privileged()  {}

func (t *spawnAgentTool) Description() string {
	return "Spawn a new orchestration run with one or more agent tasks. " +
		"Tasks can declare dependencies (depends_on) for DAG-based execution. " +
		"Use spawn_agent when you need sequential execution waves (implement Wave 1, " +
		"wait for gate, then Wave 2). For parallel independent tasks, use dispatch_tasks. " +
		"Sets wait to control whether the call returns immediately (none), waits for " +
		"one task (task), or waits for the full run (run). " +
		"When wait=run, returns the completed tasks' structured results. Otherwise returns run_id, display_name, status, and task list for subsequent " +
		"inspection (inspect_agents), joining (join_run), or cancellation (cancel_run)."
}

func (t *spawnAgentTool) Parameters() map[string]any {
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
							"description": "Unique task identifier within this run",
						},
						"name": map[string]any{
							"type":        "string",
							"description": "Handler name (e.g. 'multi_step', 'delegate', 'oneshot')",
						},
						"depends_on": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
							"description": "Task IDs this task depends on (for DAG ordering)",
						},
						"prompt": map[string]any{
							"type":        "string",
							"description": "Natural language task description for the sub-agent",
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Per-task timeout override (seconds); 0 uses config default",
						},
						"budget": map[string]any{
							"type":        "integer",
							"minimum":     0,
							"description": "Budget for this task (cost units)",
						},
					},
					"required":             []string{"id", "name", "prompt"},
					"additionalProperties": false,
				},
				"description": "Array of 1+ tasks forming a DAG via depends_on",
			},
			"idempotency_key": map[string]any{
				"type":        "string",
				"description": "Optional idempotency key to deduplicate run creation",
			},
			"wait": map[string]any{
				"type": "string", "enum": []string{"none", "task", "run"},
				"description": "Wait mode: none returns immediately; task waits for the requested task; run waits for the full run",
			},
			"wait_task_id": map[string]any{
				"type": "string", "description": "Required when wait=task",
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}
}

func (t *spawnAgentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c := initCoordinator(t.dispatcher, t.cfg, t.repo)
	var params struct {
		Tasks []struct {
			ID             string   `json:"id"`
			Name           string   `json:"name"`
			DependsOn      []string `json:"depends_on,omitempty"`
			Prompt         string   `json:"prompt"`
			TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
			Budget         int      `json:"budget,omitempty"`
		} `json:"tasks"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		Wait           string `json:"wait,omitempty"`
		WaitTaskID     string `json:"wait_task_id,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"error":"at least one task is required"}`, nil
	}
	if params.Wait == "" {
		params.Wait = "none"
	}
	if params.Wait != "none" && params.Wait != "task" && params.Wait != "run" {
		return `{"error":"wait must be one of none, task, or run"}`, nil
	}
	if params.Wait == "task" && params.WaitTaskID == "" {
		return `{"error":"wait_task_id is required when wait=task"}`, nil
	}
	batchTimeout := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)
	subTasks := make([]subagents.Task, len(params.Tasks))
	for i, pt := range params.Tasks {
		input, err := json.Marshal(pt.Prompt)
		if err != nil {
			return "", fmt.Errorf("spawn_agent: marshal input: %w", err)
		}
		taskTimeout := batchTimeout
		if pt.TimeoutSeconds > 0 {
			taskTimeout = pt.TimeoutSeconds
		}
		subTasks[i] = subagents.Task{
			ID:        pt.ID,
			Name:      pt.Name,
			Owner:     "mivia",
			Input:     input,
			DependsOn: pt.DependsOn,
			Timeout:   time.Duration(taskTimeout) * time.Second,
			Budget:    pt.Budget,
		}
	}
	handle, err := c.Spawn(ctx, subTasks, params.IdempotencyKey)
	if err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}
	snap, err := c.Inspect(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}

	storeOrchestrationHandle(snap.RunID, &orchestrationHandle{
		coord: c, handle: handle, repo: effectiveOrchestrationRepo(t.repo), dispatcher: t.dispatcher,
	})

	snap, completed, err := waitForSpawnResult(ctx, c, handle, params.Wait, params.WaitTaskID, snap)
	if err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}
	result := map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
		"tasks":        taskSummaries(snap.Tasks),
	}
	if completed != nil {
		result["task_results"] = modelTaskResults(completed.Results)
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

func waitForSpawnResult(ctx context.Context, c coordinator.Coordinator, handle *coordinator.RunHandle, mode, taskID string, initial ledger.RunSnapshot) (ledger.RunSnapshot, *coordinator.RunResult, error) {
	if mode == "run" {
		result, err := c.Join(ctx, handle)
		if err != nil {
			return ledger.RunSnapshot{}, nil, fmt.Errorf("wait for run: %w", err)
		}
		return result.Snapshot, result, nil
	}
	if mode == "task" {
		snap, err := waitForSpawn(ctx, c, handle, mode, taskID)
		return snap, nil, err
	}
	return initial, nil, nil
}

func waitForSpawn(ctx context.Context, c coordinator.Coordinator, handle *coordinator.RunHandle, mode, taskID string) (ledger.RunSnapshot, error) {
	if mode == "run" {
		if _, err := c.Join(ctx, handle); err != nil {
			return ledger.RunSnapshot{}, fmt.Errorf("wait for run: %w", err)
		}
	} else if err := waitForTask(ctx, c, handle, taskID); err != nil {
		return ledger.RunSnapshot{}, fmt.Errorf("wait for task: %w", err)
	}
	return c.Inspect(ctx, handle)
}

// Ensure spawnAgentTool implements required interfaces at compile time.
var _ tools.Tool = (*spawnAgentTool)(nil)

func (t *spawnAgentTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionExternal,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}

// ---------------------------------------------------------------------------
// inspect_agent
// ---------------------------------------------------------------------------

type inspectAgentTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *inspectAgentTool) Name() string { return "inspect_agents" }
func (t *inspectAgentTool) Privileged()  {}

func (t *inspectAgentTool) Description() string {
	return "Inspect a previously spawned orchestration run. " +
		"Returns the current run snapshot including status, task states, " +
		"timestamps, and any output/error references. " +
		"Use after spawn_agent to check progress or after join_run to see final state."
}

func (t *inspectAgentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID returned by spawn_agent",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}

func (t *inspectAgentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("inspect_agents: %w", err)
	}
	if params.RunID == "" {
		return `{"error":"run_id is required"}`, nil
	}

	rawHandle, ok := runHandles.Load(params.RunID)
	if !ok {
		return `{"error":"unknown run_id"}`, nil
	}
	record, ok := rawHandle.(*orchestrationHandle)
	if !ok || !orchestrationHandleAccessible(record, t.dispatcher, t.repo) {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := record.handle

	snap, err := record.coord.Inspect(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("inspect_agents: %w", err)
	}

	// Build a model-friendly response.
	type taskInfo struct {
		TaskID      string   `json:"task_id"`
		DisplayName string   `json:"display_name"`
		Status      string   `json:"status"`
		DependsOn   []string `json:"depends_on,omitempty"`
		OutputRef   string   `json:"output_ref,omitempty"`
		ErrorRef    string   `json:"error_ref,omitempty"`
	}
	tasks := make([]taskInfo, len(snap.Tasks))
	for i, t := range snap.Tasks {
		tasks[i] = taskInfo{
			TaskID:      t.TaskID,
			DisplayName: t.DisplayName,
			Status:      t.Status,
			DependsOn:   t.DependsOn,
			OutputRef:   t.OutputRef,
			ErrorRef:    t.ErrorRef,
		}
	}

	out, _ := json.Marshal(map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
		"tasks":        tasks,
	})
	return string(out), nil
}

func taskSummaries(tasks []ledger.TaskSnapshot) []map[string]any {
	out := make([]map[string]any, len(tasks))
	for i, task := range tasks {
		out[i] = map[string]any{
			"task_id": task.TaskID, "display_name": task.DisplayName,
			"status": task.Status, "depends_on": task.DependsOn,
		}
	}
	return out
}

func waitForTask(ctx context.Context, c coordinator.Coordinator, handle *coordinator.RunHandle, taskID string) error {
	for {
		snap, err := c.Inspect(ctx, handle)
		if err != nil {
			return err
		}
		found := false
		for _, task := range snap.Tasks {
			if task.TaskID != taskID {
				continue
			}
			found = true
			switch task.Status {
			case string(ledger.TaskStatusCompleted), string(ledger.TaskStatusFailed),
				string(ledger.TaskStatusTimedOut), string(ledger.TaskStatusCanceled),
				string(ledger.TaskStatusBlocked):
				return nil
			}
			break
		}
		if !found {
			return fmt.Errorf("unknown task_id %q", taskID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Ensure inspectAgentTool implements required interfaces at compile time.
var _ tools.Tool = (*inspectAgentTool)(nil)

func (t *inspectAgentTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionRead,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Registration helper
// ---------------------------------------------------------------------------

// registerOrchestrationTools registers the orchestration tools (spawn_agent,
// inspect_agent, join_run, cancel_run) on both the model-visible registry and
// the runtime dispatcher.  It is called from NewSessionDispatcher.
func registerOrchestrationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, repo ledger.LedgerRepository) error {
	toolSet := []tools.Tool{
		&spawnAgentTool{dispatcher: d, cfg: cfg, repo: repo},
		&inspectAgentTool{dispatcher: d, cfg: cfg, repo: repo},
		&joinRunTool{dispatcher: d, cfg: cfg, repo: repo},
		&cancelRunTool{dispatcher: d, cfg: cfg, repo: repo},
	}
	for _, t := range toolSet {
		if err := registerSessionTool(d, reg, t); err != nil {
			return err
		}
	}
	return nil
}
