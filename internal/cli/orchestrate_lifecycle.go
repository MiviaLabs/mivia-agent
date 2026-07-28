package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

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
	c := initCoordinator(t.dispatcher, t.cfg)

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
	record, ok := rawHandle.(*orchestrationHandle)
	if !ok || record.coord != c {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := record.handle

	result, err := c.Join(ctx, handle)
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
	c := initCoordinator(t.dispatcher, t.cfg)

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
	record, ok := rawHandle.(*orchestrationHandle)
	if !ok || record.coord != c {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := record.handle

	if err := c.Cancel(ctx, handle); err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}

	snap, err := c.Inspect(ctx, handle)
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
