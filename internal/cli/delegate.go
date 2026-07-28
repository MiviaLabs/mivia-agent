package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
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
	repo       ledger.LedgerRepository
	nextID     atomic.Uint64
}

func (t *delegateTool) Capability(args json.RawMessage) tools.Capability {
	// Advertise a finite orchestration budget so multi_step delegate is not
	// capped by the default 60s agent ToolTimeout.
	sec := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, delegateTimeoutOverride(args))
	return tools.Capability{
		Class:       tools.ExecutionExternal,
		ResourceKey: "delegate",
		Timeout:     time.Duration(sec) * time.Second,
	}
}

func delegateTimeoutOverride(args json.RawMessage) int {
	var params struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(args, &params)
	return params.TimeoutSeconds
}
func (t *delegateTool) Name() string { return "delegate" }
func (t *delegateTool) Description() string {
	return "Delegate a subtask to a sub-agent. " +
		"By default the sub-agent makes one LLM call (no tools) and returns structured results. " +
		"Set multi_step=true to give the sub-agent full tool access (read, write, search, run). " +
		"Use timeout_seconds to set a budget (0 uses config default or a finite safety ceiling). " +
		"Use for: analyzing code, summarizing findings, parallel research (one-shot), " +
		"or complex multi-step work needing tools (multi_step=true). " +
		"Heartbeat/progress events appear in the UI during long-running tasks. " +
		"Results include status, elapsed, and step count metadata."
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
				"description": "Timeout budget in seconds. 0 uses config default; runtime always applies a finite safety ceiling. Raise for complex multi-step work.",
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

	timeoutSec := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, params.TimeoutSeconds)
	timeout := time.Duration(timeoutSec) * time.Second

	input, _ := json.Marshal(params.Task)
	tasks := []subagents.Task{{
		ID:            "d1",
		InvocationKey: fmt.Sprintf("delegate:%d", t.nextID.Add(1)),
		Name:          handlerName,
		Owner:         "mivia",
		Input:         input,
		Timeout:       timeout,
	}}

	_, result, err := runThroughCoordinator(ctx, t.dispatcher, t.cfg, tasks, "", t.repo)
	if result != nil && result.Err != nil {
		payload, _ := json.Marshal(map[string]any{"error_ref": orchestrationReference("error", []byte(result.Err.Error())), "status": statusFromErr(result.Err)})
		return string(payload), nil
	}
	if result != nil && len(result.Results) > 0 {
		r := result.Results[0]
		if r.Err != nil {
			// Model-visible status body; nil transport err so agent loop keeps body.
			payload, _ := json.Marshal(map[string]any{
				"error_ref":  orchestrationReference("error", []byte(r.Err.Error())),
				"status":     r.Status,
				"output_ref": orchestrationReference("output", r.Output),
			})
			return string(payload), nil
		}
		if len(r.Output) > 0 {
			payload, _ := json.Marshal(map[string]any{"status": r.Status, "output_ref": orchestrationReference("output", r.Output)})
			return string(payload), nil
		}
	}
	if err != nil {
		payload, _ := json.Marshal(map[string]string{
			"error_ref": orchestrationReference("error", []byte(err.Error())),
			"status":    statusFromErr(err),
		})
		return string(payload), nil
	}
	// Last resort: result with no results and no error, or nil result and nil error.
	if result != nil {
		payload, _ := json.Marshal(map[string]any{
			"status":       result.Snapshot.Status,
			"display_name": result.Snapshot.DisplayName,
			"run_id":       result.Snapshot.RunID,
		})
		return string(payload), nil
	}
	return `{"status":"no_result"}`, nil
}

func jsonRawOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// Ensure delegateTool implements required interfaces at compile time.
var _ tools.Tool = (*delegateTool)(nil)
var _ tools.CapableTool = (*delegateTool)(nil)
