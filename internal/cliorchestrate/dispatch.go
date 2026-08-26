package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// messageSynopsis is the model-visible envelope entry for a task message.
type messageSynopsis struct {
	MessageID string `json:"message_id"`
	Kind      string `json:"kind"`
	Synopsis  string `json:"synopsis"`
}

// dispatchTasksTool implements tools.Tool by routing multiple tasks through
// the subagents.Pool with optional dependency ordering. Tasks without
// dependencies run concurrently; dependent tasks wait for prerequisites.
type dispatchTasksTool struct {
	dispatcher   *runtime.Dispatcher
	cfg          config.SubagentConfig
	repo         ledger.LedgerRepository
	skillReg     *skills.Registry
	agentReg     *agents.AgentRegistry
	providerName string
	model        string
	nextBatch    atomic.Uint64
}

func (t *dispatchTasksTool) Capability(args json.RawMessage) tools.Capability {
	// Capability.Timeout is the parent agent-loop budget for this tool call.
	// It may exceed the default 60s ToolTimeout so multi-step batches are not
	// killed early; EffectiveTimeoutSec still keeps a finite safety ceiling.
	return tools.Capability{
		Class:       tools.ExecutionExternal,
		ResourceKey: ToolDispatchTasks,
		Timeout:     time.Duration(DispatchOrchestrationSec(t.cfg.DefaultTimeout, args)) * time.Second,
	}
}

func (t *dispatchTasksTool) Name() string { return ToolDispatchTasks }
func (t *dispatchTasksTool) Privileged()  {}
func (t *dispatchTasksTool) Description() string {
	desc := "Execute multiple sub-tasks in PARALLEL. Use this for ALL research, code reviews, " +
		"bug audits, and any work that can be split - never do N sequential passes. " +
		"Each task must explicitly select one authorized agent and may optionally select a skill under that agent's policy. " +
		"Tasks without dependencies (depends_on) run concurrently. " +
		"Every task always reports its own result and status, so one failure never " +
		"costs you the others. " +
		"Recommended: 2-4 tasks at once. " +
		"If dispatch_tasks fails, retry with fewer tasks or switch to spawn_agent. " +
		"Use timeout_seconds to set a per-batch budget. " +
		"Results include each task's structured output, correlation reference, status (completed/failed/timed_out/canceled), elapsed, steps, and step_count. " +
		"For large results, output_ref is returned instead of inline output; use ledger_read to fetch the full body. " +
		"Heartbeat/progress events appear in the UI during long-running tasks."
	return desc
}

func (t *dispatchTasksTool) Parameters() map[string]any {
	result := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array", "items": taskItemSchema(t.agentReg, true),
				"description": "Array of 1-16 tasks. Tasks without depends_on run concurrently.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Per-task timeout budget in seconds. " + TimeoutHint(),
			},
			"wait": map[string]any{
				"type": "string", "enum": []string{"none", "task", "run"},
				"description": "Wait mode: run (default) blocks until the whole batch finishes and returns each task's result; none returns immediately with a run_id to inspect/join/cancel; task waits for the requested wait_task_id only",
			},
			"wait_task_id": map[string]any{
				"type": "string", "description": "Required when wait=task",
			},
			"idempotency_key": map[string]any{
				"type":        "string",
				"description": "Optional key to deduplicate identical batch dispatch for the same caller",
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}

	return result
}

func (t *dispatchTasksTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Tasks          []dispatchTaskParam `json:"tasks"`
		TimeoutSeconds int                 `json:"timeout_seconds,omitempty"`
		IdempotencyKey string              `json:"idempotency_key,omitempty"`
		Wait           string              `json:"wait,omitempty"`
		WaitTaskID     string              `json:"wait_task_id,omitempty"`
	}
	if err := decodeStrictTaskJSON(args, &params); err != nil {
		return "", fmt.Errorf("dispatch_tasks: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"tasks":[]}`, nil
	}
	wait, err := normalizedDispatchWait(params.Wait, params.WaitTaskID)
	if err != nil {
		return `{"error":"` + err.Error() + `"}`, nil
	}

	// Always resolve a positive batch timeout so multi_step / pool work is
	// bounded even when default_timeout_seconds is 0. An explicit
	// timeout_seconds IS the budget — not floored to the 12h default. Per-task
	// overrides can still raise the batch budget so a task never outlives it.
	overrides := make([]int, 0, len(params.Tasks))
	for _, p := range params.Tasks {
		if p.TimeoutSeconds > 0 {
			overrides = append(overrides, p.TimeoutSeconds)
		}
	}
	batchTimeout := config.RequestedTimeoutSec(t.cfg.DefaultTimeout, params.TimeoutSeconds, overrides...)

	tasks, err := t.buildTasks(params.Tasks, batchTimeout)
	if err != nil {
		return "", err
	}

	// wait != "run" (async): the same spawn+register+wait path spawn_agent
	// uses, and the same {"run_id":...,"task_results":[...]} envelope - a
	// caller that asked NOT to block for the batch gets a run to inspect/
	// join/cancel, not a per-task array that would misleadingly imply the
	// batch already finished.
	if wait != "run" {
		snap, completed, err := spawnAndWait(ctx, t.dispatcher, t.cfg, t.repo, tasks, params.IdempotencyKey, wait, params.WaitTaskID)
		if err != nil {
			return "", fmt.Errorf("dispatch_tasks: %w", err)
		}
		return spawnResultPayload(snap, completed, t.cfg.InlineOutputBytes, EffectiveOrchestrationRepo(t.repo)), nil
	}

	_, runResult, err := RunThroughCoordinator(ctx, t.dispatcher, t.cfg, tasks, params.IdempotencyKey, t.repo)
	return t.encodeSyncRunResult(runResult, err), nil
}

// encodeSyncRunResult builds the wait="run" response body: the bare per-task
// array whenever any result exists (a failed or blocked task is already
// visible per task, and run_error/status would only explain the run
// itself), falling back to a run-level error envelope only when there is no
// task result to report at all. Never returns a Go error: the agent loop
// wipes an empty body to a bare "error: …" string, so every path here must
// answer with a model-visible JSON body.
func (t *dispatchTasksTool) encodeSyncRunResult(runResult *coordinator.RunResult, runErr error) string {
	var results []subagents.Result
	if runResult != nil {
		results = runResult.Results
	}
	if len(results) > 0 {
		return t.encodeResults(snapshotTasks(runResult), results)
	}
	// No error_ref on either branch below: a run-level failure (spawn,
	// validation, join, timeout) is never a task's recorded error, so
	// nothing was ever stored under its digest. The full text is inline in
	// "error" instead.
	if runResult != nil && runResult.Err != nil {
		payload, _ := json.Marshal(map[string]string{
			"error":  runResult.Err.Error(),
			"status": StatusFromErr(runResult.Err),
		})
		return string(payload)
	}
	if runErr != nil {
		payload, _ := json.Marshal(map[string]string{
			"error":  runErr.Error(),
			"status": StatusFromErr(runErr),
		})
		return string(payload)
	}
	return `{"tasks":[]}`
}

// StatusFromErr maps a Go error to the matching ledger task status string.
func StatusFromErr(err error) string {
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

// dispatchTaskParam is one model-authored task in a dispatch_tasks call.
type dispatchTaskParam struct {
	ID             string         `json:"id"`
	Prompt         string         `json:"prompt"`
	DependsOn      []string       `json:"depends_on,omitempty"`
	Agent          string         `json:"agent,omitempty"`
	Skill          string         `json:"skill,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Budget         int            `json:"budget,omitempty"`
	OutputSchema   map[string]any `json:"output_schema,omitempty"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

func (t *dispatchTasksTool) buildTasks(params []dispatchTaskParam, batchTimeout int) ([]subagents.Task, error) {
	tasks := make([]subagents.Task, len(params))
	batchID := fmt.Sprintf("dispatch:%d", t.nextBatch.Add(1))
	for i, pt := range params {
		route, err := ResolveTaskRoute(t.agentReg, t.skillReg, pt.Agent, pt.Skill)
		if err != nil {
			return nil, fmt.Errorf("dispatch_tasks: %w", err)
		}
		input, _ := json.Marshal(pt.Prompt)
		// Per-task timeout is raise-only relative to the batch budget:
		// a task may extend, never shrink, the batch budget (which already
		// reflects an explicit timeout_seconds or the 12h default), and the
		// MaxTimeoutSeconds clamp stops a huge model-supplied timeout_seconds
		// from wrapping time.Duration negative (R2B-1).
		taskTimeout := config.EffectiveTimeoutSec(batchTimeout, pt.TimeoutSeconds)
		name, agentName, digest, providerName, model := routedTaskIdentity(route, t.providerName, t.model)
		outSchema, inSchema, err := resolveTaskSchemas(pt.OutputSchema, pt.InputSchema, route, t.skillReg)
		if err != nil {
			return nil, fmt.Errorf("dispatch_tasks: task %q: %w", pt.ID, err)
		}
		if inSchema != nil {
			if err := validateTaskInput(inSchema, input); err != nil {
				return nil, fmt.Errorf("dispatch_tasks: task %q: %w", pt.ID, err)
			}
		}
		tasks[i] = subagents.Task{
			ID: pt.ID, InvocationKey: batchID + ":" + pt.ID,
			Name: name, AgentName: agentName, AgentDigest: digest,
			Skill: route.skill, Owner: DefaultToolOwner,
			ProviderName: providerName, Model: model,
			Input: input, DependsOn: pt.DependsOn,
			Timeout:      time.Duration(taskTimeout) * time.Second,
			Budget:       pt.Budget,
			OutputSchema: outSchema, InputSchema: inSchema,
		}
	}
	return tasks, nil
}

// NewDispatchTasksTool creates a dispatch_tasks tool. RegisterOrchestrationTools
// builds the dispatchTasksTool struct directly instead of calling this
// constructor; it is kept for other callers that need a standalone instance.
func NewDispatchTasksTool(d *runtime.Dispatcher, cfg config.SubagentConfig, skillReg *skills.Registry, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry, providerName, model string) tools.Tool {
	return &dispatchTasksTool{
		dispatcher:   d,
		cfg:          cfg,
		skillReg:     skillReg,
		repo:         repo,
		agentReg:     agentReg,
		providerName: providerName,
		model:        model,
	}
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
