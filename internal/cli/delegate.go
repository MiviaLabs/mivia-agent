package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// delegateTool implements tools.Tool by routing a single task through the
// subagents.Pool to a one-shot LLM handler. The model calls it as a
// regular tool; internally it creates a Pool, executes one task, and
// returns the structured result.
type delegateTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
}

func (t *delegateTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, ResourceKey: "delegate"}
}
func (t *delegateTool) Name() string { return "delegate" }
func (t *delegateTool) Description() string {
	return "Delegate a subtask to a focused sub-agent. " +
		"The sub-agent makes one LLM call (no tools) and returns structured results. " +
		"Use for parallel research, independent analysis, or scoped subtasks that benefit from isolation."
}
func (t *delegateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "Natural language task description for the sub-agent. Include all necessary context.",
			},
		},
		"required":             []string{"task"},
		"additionalProperties": false,
	}
}
func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("delegate: %w", err)
	}
	if params.Task == "" {
		return "", fmt.Errorf("delegate: task is required")
	}

	pool := subagents.New(t.dispatcher, subagents.Policy{
		Workers:  t.cfg.MaxWorkers,
		MaxDepth: t.cfg.MaxDepth,
		Timeout:  time.Duration(t.cfg.DefaultTimeout) * time.Second,
	})

	input, _ := json.Marshal(params.Task)
	tasks := []subagents.Task{{
		ID:      "d1",
		Name:    "delegate",
		Owner:   "mivia",
		Input:   input,
		Timeout: time.Duration(t.cfg.DefaultTimeout) * time.Second,
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
