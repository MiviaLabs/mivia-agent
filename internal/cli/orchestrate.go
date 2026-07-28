// Package cli provides orchestration tool implementations that bridge the
// model-facing tool set with the coordinator's async run model.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Package-level coordinator singleton, lazily initialized on first use of any
// orchestration tool. runHandles maps runID → *coordinator.RunHandle for
// subsequent Inspect/Join/Cancel calls.
var (
	coord      *coordinator.Coordinator
	coordOnce  sync.Once
	runHandles sync.Map // runID → *coordinator.RunHandle
)

// initCoordinator lazily creates the Coordinator singleton with an in-memory
// ledger repository and a subagent pool backed by the given dispatcher.
// Safe for concurrent calls; only the first invocation initialises the
// singleton.  Subsequent calls are no-ops.
func initCoordinator(d *runtime.Dispatcher, cfg config.SubagentConfig) {
	coordOnce.Do(func() {
		repo := ledger.NewMemoryLedgerRepository()
		pool := subagents.New(d, subagents.Policy{
			Workers:   cfg.MaxWorkers,
			MaxDepth:  cfg.MaxDepth,
			MaxFanout: cfg.MaxFanout,
			Timeout:   time.Duration(cfg.DefaultTimeout) * time.Second,
		})
		coord = coordinator.New(repo, pool)
	})
}

// ---------------------------------------------------------------------------
// spawn_agent
// ---------------------------------------------------------------------------

type spawnAgentTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
}

func (t *spawnAgentTool) Name() string { return "spawn_agent" }

func (t *spawnAgentTool) Description() string {
	return "Spawn a new orchestration run with one or more agent tasks. " +
		"Tasks can declare dependencies (depends_on) for DAG-based execution. " +
		"Returns run_id, display_name, status, and task list for subsequent " +
		"inspection (inspect_agent), joining (join_run), or cancellation (cancel_run)."
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
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}
}

func (t *spawnAgentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	initCoordinator(t.dispatcher, t.cfg)

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
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"error":"at least one task is required"}`, nil
	}

	batchTimeout := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)

	subTasks := make([]subagents.Task, len(params.Tasks))
	for i, pt := range params.Tasks {
		input, _ := json.Marshal(pt.Prompt)
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

	handle, err := coord.Spawn(ctx, subTasks, params.IdempotencyKey)
	if err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}

	// Capture initial snapshot to get the run ID from the ledger.
	snap, err := coord.Inspect(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("spawn_agent: %w", err)
	}

	// Store handle for later Inspect/Join/Cancel by run ID.
	runHandles.Store(snap.RunID, handle)

	out, _ := json.Marshal(map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
		"task_count":   len(snap.Tasks),
	})
	return string(out), nil
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
}

func (t *inspectAgentTool) Name() string { return "inspect_agent" }

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
	initCoordinator(t.dispatcher, t.cfg)

	var params struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("inspect_agent: %w", err)
	}
	if params.RunID == "" {
		return `{"error":"run_id is required"}`, nil
	}

	rawHandle, ok := runHandles.Load(params.RunID)
	if !ok {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := rawHandle.(*coordinator.RunHandle)

	snap, err := coord.Inspect(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("inspect_agent: %w", err)
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

// Ensure inspectAgentTool implements required interfaces at compile time.
var _ tools.Tool = (*inspectAgentTool)(nil)

func (t *inspectAgentTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionRead,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}

// ---------------------------------------------------------------------------
// join_run
// ---------------------------------------------------------------------------

type joinRunTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
}

func (t *joinRunTool) Name() string { return "join_run" }

func (t *joinRunTool) Description() string {
	return "Join (block until) a previously spawned orchestration run completes. " +
		"Returns the final run result including per-task status, output " +
		"references, and any errors. Blocks until the run finishes or the " +
		"calling context is canceled."
}

func (t *joinRunTool) Parameters() map[string]any {
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

func (t *joinRunTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	initCoordinator(t.dispatcher, t.cfg)

	var params struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("join_run: %w", err)
	}
	if params.RunID == "" {
		return `{"error":"run_id is required"}`, nil
	}

	rawHandle, ok := runHandles.Load(params.RunID)
	if !ok {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := rawHandle.(*coordinator.RunHandle)

	result, err := coord.Join(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("join_run: %w", err)
	}

	type taskResultInfo struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Output string `json:"output,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	taskResults := make([]taskResultInfo, len(result.Results))
	for i, r := range result.Results {
		taskResults[i] = taskResultInfo{
			TaskID: r.TaskID,
			Status: r.Status,
		}
		if r.Err != nil {
			taskResults[i].Error = r.Err.Error()
		} else if len(r.Output) > 0 {
			taskResults[i].Output = string(r.Output)
		}
	}

	runErr := ""
	if result.Err != nil {
		runErr = result.Err.Error()
	}

	out, _ := json.Marshal(map[string]any{
		"run_id":       result.Snapshot.RunID,
		"display_name": result.Snapshot.DisplayName,
		"status":       result.Snapshot.Status,
		"run_error":    runErr,
		"task_results": taskResults,
	})
	return string(out), nil
}

// Ensure joinRunTool implements required interfaces at compile time.
var _ tools.Tool = (*joinRunTool)(nil)

func (t *joinRunTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionExternal,
		Timeout: 3 * time.Hour, // long-running wait
	}
}

// ---------------------------------------------------------------------------
// cancel_run
// ---------------------------------------------------------------------------

type cancelRunTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
}

func (t *cancelRunTool) Name() string { return "cancel_run" }

func (t *cancelRunTool) Description() string {
	return "Cancel a previously spawned orchestration run. " +
		"Tasks that are queued or running will be marked as canceled. " +
		"Already completed tasks retain their results. " +
		"Returns the final run snapshot after cancellation."
}

func (t *cancelRunTool) Parameters() map[string]any {
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

func (t *cancelRunTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	initCoordinator(t.dispatcher, t.cfg)

	var params struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}
	if params.RunID == "" {
		return `{"error":"run_id is required"}`, nil
	}

	rawHandle, ok := runHandles.Load(params.RunID)
	if !ok {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := rawHandle.(*coordinator.RunHandle)

	if err := coord.Cancel(ctx, handle); err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}

	snap, err := coord.Inspect(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}

	out, _ := json.Marshal(map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
	})
	return string(out), nil
}

// Ensure cancelRunTool implements required interfaces at compile time.
var _ tools.Tool = (*cancelRunTool)(nil)

func (t *cancelRunTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionWrite,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Registration helper
// ---------------------------------------------------------------------------

// registerOrchestrationTools registers the orchestration tools (spawn_agent,
// inspect_agent, join_run, cancel_run) on both the model-visible registry and
// the runtime dispatcher.  It is called from NewSessionDispatcher.
func registerOrchestrationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig) error {
	toolSet := []tools.Tool{
		&spawnAgentTool{dispatcher: d, cfg: cfg},
		&inspectAgentTool{dispatcher: d, cfg: cfg},
		&joinRunTool{dispatcher: d, cfg: cfg},
		&cancelRunTool{dispatcher: d, cfg: cfg},
	}
	for _, t := range toolSet {
		if err := registerSessionTool(d, reg, t); err != nil {
			return err
		}
	}
	return nil
}
