package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
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
		ResourceKey: handlerDelegate,
		Timeout:     time.Duration(sec+dispatchOrchestrationSlackSec) * time.Second,
	}
}

func delegateTimeoutOverride(args json.RawMessage) int {
	var params struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(args, &params)
	return params.TimeoutSeconds
}
func (t *delegateTool) Name() string { return handlerDelegate }
func (t *delegateTool) Privileged()  {}
func (t *delegateTool) Description() string {
	return "Delegate a SINGLE focused subtask to a sub-agent. Use delegate for isolated fixes or " +
		"narrow analysis that does not need parallelism. For multiple independent tasks, use " +
		"dispatch_tasks. For complex multi-step work needing file access, set multi_step=true. " +
		"By default the sub-agent makes one LLM call (no tools) and returns structured results. " +
		"Set multi_step=true to give the sub-agent full tool access (read, write, search, run). " +
		"Use timeout_seconds to set a budget: " + timeoutHint() + " " +
		"Use for: analyzing code, summarizing findings, parallel research (one-shot), " +
		"or complex multi-step work needing tools (multi_step=true). " +
		"Heartbeat/progress events appear in the UI during long-running tasks. " +
		"Results include the sub-agent's structured output, a correlation reference, status, elapsed, and step count metadata. " +
		"For large results, output_ref is returned instead of inline output; use ledger_read to fetch the full body."
}
func (t *delegateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "Natural language task description for the sub-agent",
			},
			handlerMultiStep: map[string]any{
				"type":        "boolean",
				"description": "When true, the sub-agent gets full tool access (multi-step). Default false (one-shot LLM call, no tools).",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Timeout budget in seconds. " + timeoutHint(),
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

	handlerName := handlerDelegate
	if params.MultiStep {
		handlerName = handlerMultiStep
	}

	timeoutSec := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, params.TimeoutSeconds)
	timeout := time.Duration(timeoutSec) * time.Second

	input, _ := json.Marshal(params.Task)
	tasks := []subagents.Task{{
		ID:            "d1",
		InvocationKey: fmt.Sprintf("delegate:%d", t.nextID.Add(1)),
		Name:          handlerName,
		Owner:         defaultToolOwner,
		Input:         input,
		Timeout:       timeout,
	}}

	_, result, err := runThroughCoordinator(ctx, t.dispatcher, t.cfg, tasks, "", t.repo)
	// The result payload is attempted first, mirroring dispatch_tasks. A
	// run-level error can be a pure persistence failure (e.g. the content write
	// for an otherwise successful task), and that must not destroy a result the
	// sub-agent actually produced. Only fall back to the run-error envelope when
	// there is no task result to report at all.
	if payload, ok := delegateResultPayload(result, t.cfg.InlineOutputBytes); ok {
		return payload, nil
	}
	// No error_ref on either envelope below: a run-level failure is never a
	// task's recorded error, so nothing was stored under its digest. The full
	// text is inline in "error" instead.
	if result != nil && result.Err != nil {
		payload, _ := json.Marshal(map[string]any{
			"error":  result.Err.Error(),
			"status": statusFromErr(result.Err),
		})
		return string(payload), nil
	}
	if err != nil {
		payload, _ := json.Marshal(map[string]string{
			"error":  err.Error(),
			"status": statusFromErr(err),
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

// delegateResultPayload builds the model-visible body for delegate's single
// task. References are read off the run snapshot, which records only the
// references whose content write actually succeeded; a ref omitted there is
// omitted here rather than re-minted into a key nothing was stored under.
//
// threshold controls the inline-by-reference switch, matching encodeResults.
func delegateResultPayload(result *coordinator.RunResult, threshold int) (string, bool) {
	if result == nil || len(result.Results) == 0 {
		return "", false
	}
	r := result.Results[0]
	outputRef, errorRef := storedResultRefs(result.Snapshot.Tasks, r)
	if r.Err != nil {
		// Model-visible status body; nil transport err so agent loop keeps body.
		payloadData := map[string]any{"status": r.Status}
		if belowInlineThreshold([]byte(r.Err.Error()), threshold, errorRef) {
			payloadData["error"] = r.Err.Error()
			addRef(payloadData, "error_ref", errorRef)
		} else {
			payloadData["error_ref"] = errorRef
		}
		if len(r.Output) > 0 {
			mergeOutputFields(payloadData, r.Output, outputRef, threshold)
		}
		payload, _ := json.Marshal(payloadData)
		return string(payload), true
	}
	if len(r.Output) == 0 {
		return "", false
	}
	payloadData := map[string]any{"status": r.Status}
	mergeOutputFields(payloadData, r.Output, outputRef, threshold)
	payload, _ := json.Marshal(payloadData)
	return string(payload), true
}

// mergeOutputFields applies the inline-by-reference threshold to a map-based
// payload, populating output/output_ref or output_ref/output_bytes/synopsis/
// read_hint as appropriate. Used by delegate which builds map[string]any
// payloads rather than struct-based ones.
func mergeOutputFields(payload map[string]any, output []byte, outputRef string, threshold int) {
	if belowInlineThreshold(output, threshold, outputRef) {
		payload["output"] = modelVisibleOutput(output)
		addRef(payload, "output_ref", outputRef)
	} else {
		payload["output_ref"] = outputRef
		payload["output_bytes"] = len(output)
		payload["synopsis"] = synopsize(output)
		if hint := readHint(threshold, len(output), outputRef); hint != "" {
			payload["read_hint"] = hint
		}
	}
	// Surface schema status from the multi_step envelope when present.
	var parsed map[string]any
	if json.Unmarshal(output, &parsed) == nil {
		if s, ok := parsed["schema"].(string); ok && s != "" {
			payload["schema"] = s
		}
	}
}

func addRef(payload map[string]any, key, ref string) {
	if ref != "" {
		payload[key] = ref
	}
}

// modelVisibleOutput returns the handler response as a JSON value when valid,
// otherwise as text. The accompanying reference is a resolvable handle to the
// persisted content, and the actual result is also included inline in this
// response while the completed run is in memory.
func modelVisibleOutput(raw json.RawMessage) any {
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	return string(raw)
}

// Ensure delegateTool implements required interfaces at compile time.
var _ tools.Tool = (*delegateTool)(nil)
var _ tools.CapableTool = (*delegateTool)(nil)
