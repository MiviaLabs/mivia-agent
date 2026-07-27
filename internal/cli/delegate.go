package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// delegateTool implements tools.Tool by routing a task through the
// subagents.Pool to either a one-shot LLM handler (single LLM call, no tools)
// or a multi-step handler (full agent loop with tool access).
//
// Usage:
//
//	delegate(task="analyze auth module")             → one-shot (1 LLM call)
//	delegate(task="refactor auth module", multi_step=true) → multi-step with tools
type delegateTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	nextID     atomic.Uint64
}

func (t *delegateTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, ResourceKey: "delegate"}
}
func (t *delegateTool) Name() string { return "delegate" }
func (t *delegateTool) Description() string {
	return "Delegate a subtask to a sub-agent. " +
		"By default the sub-agent makes one LLM call (no tools) and returns structured results. " +
		"Set multi_step=true to give the sub-agent full tool access (read, write, search, run). " +
		"Use timeout_seconds to override the default timeout (0 = no timeout). " +
		"Use for: analyzing code, summarizing findings, parallel research (one-shot), " +
		"or complex multi-step work needing tools (multi_step=true). " +
		"Heartbeat/progress events appear in the UI during long-running tasks. " +
		"Results include elapsed time and step count metadata."
}
func (t *delegateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "Natural language task description for the sub-agent",
			},
			"multi_step": map[string]any{
				"type":        "boolean",
				"description": "When true, the sub-agent gets full tool access (multi-step). Default false (one-shot LLM call, no tools).",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Override default timeout (default 0, 0 = no timeout). Set higher for complex multi-step tasks.",
			},
		},
		"required":             []string{"task"},
		"additionalProperties": false,
	}
}
func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Task           string `json:"task"`
		MultiStep      bool   `json:"multi_step,omitempty"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("delegate: %w", err)
	}
	if params.Task == "" {
		return "", fmt.Errorf("delegate: task is required")
	}

	handlerName := "delegate"
	if params.MultiStep {
		handlerName = "multi_step"
	}

	timeout := t.cfg.DefaultTimeout
	if params.TimeoutSeconds > 0 {
		timeout = params.TimeoutSeconds
	}

	pool := subagents.New(t.dispatcher, subagents.Policy{
		Workers:  t.cfg.MaxWorkers,
		MaxDepth: t.cfg.MaxDepth,
		Timeout:  time.Duration(timeout) * time.Second,
	})

	input, _ := json.Marshal(params.Task)
	tasks := []subagents.Task{{
		ID:            "d1",
		InvocationKey: fmt.Sprintf("delegate:%d", t.nextID.Add(1)),
		Name:          handlerName,
		Owner:         "mivia",
		Input:         input,
		Timeout:       time.Duration(timeout) * time.Second,
	}}

	results, err := pool.Run(ctx, tasks)
	if err != nil {
		return fmt.Sprintf(`{"error":"%v"}`, err), err
	}
	if len(results) == 0 {
		return `{"status":"no_result"}`, nil
	}
	r := results[0]
	if r.Err != nil {
		return fmt.Sprintf(`{"error":"%v"}`, r.Err), r.Err
	}
	return string(r.Output), nil
}

// Ensure delegateTool implements required interfaces at compile time.
var _ tools.Tool = (*delegateTool)(nil)
var _ tools.CapableTool = (*delegateTool)(nil)
