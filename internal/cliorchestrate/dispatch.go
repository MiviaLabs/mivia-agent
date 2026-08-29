package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// ContentRef resolves the pinned full body via ledger_read when the
	// synopsis is not enough (INV-AG-10: the event's content_ref always
	// resolves). Empty on legacy events written before the field existed.
	ContentRef string `json:"content_ref,omitempty"`
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
		"bug audits, and any work that can be split - never do N sequential passes. "
	// Same invariant as agentRoutingDescription: the always-available claim
	// only appears when the built-in actually resolved into the registry (a
	// same-name skill collision can skip it), so the prose never promises a
	// target the enum lacks.
	if _, ok := t.agentReg.Get(agents.BuiltInGeneralPurposeName); ok {
		desc += "The agent field is optional: the built-in general-purpose agent is always available and carries " +
			"the default toolset; omitting agent runs a tool-less one-shot call, so name an agent for any task that needs tools. "
	} else {
		desc += "The agent field is optional: name a listed agent for any task that needs tools; " +
			"omitting agent runs a tool-less one-shot call. "
	}
	desc += "Tasks without dependencies (depends_on) run concurrently. " +
		"Every task always reports its own result and status, so one failure never " +
		"costs you the others. " +
		"Recommended: 2-4 tasks at once. " +
		"If dispatch_tasks fails, retry with fewer tasks. " +
		"Use timeout_seconds to set a per-batch budget. " +
		"The batch budget is clamped to this session's remaining turn deadline - a batch you cannot wait for fails fast instead of dying mid-wait. " +
		"In interactive sessions prefer wait:\"none\" and poll inspect_agents instead of long blocking waits. " +
		"Error envelopes include run_id, so a canceled or timed-out batch stays reachable via inspect_agents/cancel_run. " +
		"Results include each task's structured output, correlation reference, status (completed/failed/timed_out/canceled), elapsed, steps, and step_count. " +
		"For large results, output_ref is returned instead of inline output; use ledger_read to fetch the full body. " +
		"Each task's recorded tool activity is behind tool_calls_ref (not inlined); use ledger_read to page it on demand. " +
		"References may be quoted in reports so a later task or agent can read the same content. " +
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

	batchTimeout, earlyOut := t.resolveBatchTimeout(ctx, params.Tasks, params.TimeoutSeconds)
	if earlyOut != "" {
		return earlyOut, nil
	}

	// namespace is harness-derived (the tool call's own ToolCallID, or an
	// internal counter with no call context), never model-supplied: it is
	// both the per-task ID prefix (below) and, unchanged, the run's
	// idempotency key - dispatch_tasks does not expose idempotency_key in
	// its model-facing schema, on purpose (see dispatchNamespace).
	namespace := t.dispatchNamespace(ctx)
	tasks, err := t.buildTasks(namespace, params.Tasks, batchTimeout)
	if err != nil {
		return "", err
	}

	// wait_task_id names a sibling task in THIS batch by its raw model-
	// supplied id, the same way depends_on does (buildTasks translates
	// that one) - the model has no way to know the namespace prefix in
	// advance, so it can only ever hand back the raw id it wrote in
	// tasks[i].id.
	waitTaskID := namespacedTaskID(namespace, params.WaitTaskID)

	// wait != "run" (async): the same spawn+register+wait path spawn_agent
	// uses, and the same {"run_id":...,"task_results":[...]} envelope - a
	// caller that asked NOT to block for the batch gets a run to inspect/
	// join/cancel, not a per-task array that would misleadingly imply the
	// batch already finished.
	// Every return from here on is model-visible (a JSON result body, or
	// an error's text) and may embed a real, namespaced task id - a
	// validation error from Pool.validate ("missing dependency", a
	// blocked/panicked task's own error text) or a per-task "task_id" in
	// the result envelope. stripNamespace recovers the raw id the model
	// itself wrote everywhere one of those ids appears, so the model
	// never has to recognize an id it never chose.
	if wait != "run" {
		snap, completed, err := spawnAndWait(ctx, t.dispatcher, t.cfg, t.repo, tasks, namespace, wait, waitTaskID)
		if err != nil {
			return "", fmt.Errorf("dispatch_tasks: %s", stripNamespace(namespace, err.Error()))
		}
		payload := spawnResultPayload(snap, completed, t.cfg.InlineOutputBytes, EffectiveOrchestrationRepo(t.repo))
		return stripNamespace(namespace, payload), nil
	}

	snap, runResult, err := RunThroughCoordinator(ctx, t.dispatcher, t.cfg, tasks, namespace, t.repo)
	return stripNamespace(namespace, t.encodeSyncRunResult(snap, runResult, err)), nil
}

// resolveBatchTimeout computes the batch's positive timeout budget, then
// clamps it to the caller's remaining context deadline (BUG-B fix):
// config.RequestedTimeoutSec below considers only config and the caller's
// requested budgets, so a dispatch inside a shorter-lived turn could ask
// workers to run far past the moment the parent context dies - the pool
// would then cancel every task as orphaned (RunThroughCoordinator's
// Join-error path) after the parent already sat silently waiting for work
// that could never be reported. Mirrors the parked-wait clamping in
// internal/clichat/messaging_tools.go.
//
// earlyOut is non-empty only when the caller's context has already
// expired: fail fast with a structured, actionable body instead of
// spawning side-effecting subagents nothing can ever consume (same
// fail-closed reasoning as RunThroughCoordinator) - the caller must
// return (earlyOut, nil) immediately without building or spawning tasks.
func (t *dispatchTasksTool) resolveBatchTimeout(ctx context.Context, tasks []dispatchTaskParam, requestedSeconds int) (timeout int, earlyOut string) {
	// timeout_seconds IS the budget when explicit - never floored to the
	// 12h default. Per-task overrides can still raise the batch budget so
	// a task never outlives it.
	overrides := make([]int, 0, len(tasks))
	for _, p := range tasks {
		if p.TimeoutSeconds > 0 {
			overrides = append(overrides, p.TimeoutSeconds)
		}
	}
	timeout = config.RequestedTimeoutSec(t.cfg.DefaultTimeout, requestedSeconds, overrides...)
	deadline, ok := ctx.Deadline()
	if !ok {
		return timeout, ""
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		payload, _ := json.Marshal(map[string]string{
			"error":  "caller context already expired; no tasks were started",
			"status": string(ledger.TaskStatusCanceled),
		})
		return timeout, string(payload)
	}
	if int(remaining.Seconds()) < timeout {
		timeout = max(1, int(remaining.Seconds()))
	}
	return timeout, ""
}

// encodeSyncRunResult builds the wait="run" response body: the bare per-task
// array whenever any result exists (a failed or blocked task is already
// visible per task, and run_error/status would only explain the run
// itself), falling back to a run-level error envelope only when there is no
// task result to report at all. Never returns a Go error: the agent loop
// wipes an empty body to a bare "error: …" string, so every path here must
// answer with a model-visible JSON body.
//
// BUG-C fix: every error branch carries snap.RunID (the orchestration handle
// was registered before Join - see storeOrchestrationHandle) plus a hint,
// so a caller whose batch died on cancel/timeout can still reach the run via
// inspect_agents / cancel_run instead of losing the only pointer to it.
func (t *dispatchTasksTool) encodeSyncRunResult(snap ledger.RunSnapshot, runResult *coordinator.RunResult, runErr error) string {
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
			"run_id": snap.RunID,
			"hint":   "inspect_agents or cancel_run can reach this run by run_id",
		})
		return string(payload)
	}
	if runErr != nil {
		payload, _ := json.Marshal(map[string]string{
			"error":  runErr.Error(),
			"status": StatusFromErr(runErr),
			"run_id": snap.RunID,
			"hint":   "inspect_agents or cancel_run can reach this run by run_id",
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

func (t *dispatchTasksTool) buildTasks(namespace string, params []dispatchTaskParam, batchTimeout int) ([]subagents.Task, error) {
	tasks := make([]subagents.Task, len(params))
	for i, pt := range params {
		// id is declared required by taskItemSchema, but decodeStrictTaskJSON
		// only rejects unknown fields - JSON Schema "required" is advisory to
		// the model, never enforced on decode. A task the model left
		// unnamed used to fall through to namespacedTaskID(namespace, "")
		// (an empty rawID short-circuits to ""), so subagents.Task.ID stayed
		// "" all the way to coordinator.createTask, which then minted an
		// unrelated random ID via newTaskID() ("anonymous task... will be
		// assigned" in coordinator/validation.go - support meant for single-
		// task spawns, not a batch). The UI's row list, built independently
		// from the model's own JSON args (events.go's dispatchTaskIDsAndNames),
		// has no way to learn that random ID and instead guesses a
		// position-based placeholder ("task-N") that never matches the real
		// Origin.TaskID on any later progress/heartbeat/done event. That one
		// row then never advances past Step 0 and eventually reads
		// "stalled" - not a stall, a permanently unattributed row. Failing
		// fast here, before any subagent spawns, turns a silently stuck
		// batch member into an immediate, actionable tool error the model
		// can retry from.
		if strings.TrimSpace(pt.ID) == "" {
			return nil, fmt.Errorf("dispatch_tasks: task %d: id is required (every task needs a unique id so its progress can be tracked)", i+1)
		}
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
			ID: namespacedTaskID(namespace, pt.ID), RawID: pt.ID, InvocationKey: namespace + ":" + pt.ID,
			Name: name, AgentName: agentName, AgentDigest: digest,
			Skill: route.skill, Owner: DefaultToolOwner,
			ProviderName: providerName, Model: model,
			// DependsOn names sibling tasks in THIS SAME batch by their raw
			// model-supplied id - the only ids the model can know before any
			// task has run - so it takes the same namespace prefix as ID
			// above, or Pool.validate's dependency lookup (keyed on the
			// namespaced ID) would never find it.
			Input: input, DependsOn: namespacedDependsOn(namespace, pt.DependsOn),
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
