package cli

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
		ResourceKey: toolDispatchTasks,
		Timeout:     time.Duration(dispatchOrchestrationSec(t.cfg.DefaultTimeout, args)) * time.Second,
	}
}

func (t *dispatchTasksTool) Name() string { return toolDispatchTasks }
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
				"type": "array", "items": taskItemSchema(t.agentReg, false, true),
				"description": "Array of 1-16 tasks. Tasks without depends_on run concurrently.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Per-task timeout budget in seconds. " + timeoutHint(),
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
	}
	if err := decodeStrictTaskJSON(args, &params); err != nil {
		return "", fmt.Errorf("dispatch_tasks: %w", err)
	}
	if len(params.Tasks) == 0 {
		return `{"tasks":[]}`, nil
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
		// and it is identical to the success branch below - kept separate only so
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

// dispatchTaskParam is one model-authored task in a dispatch_tasks call.
type dispatchTaskParam struct {
	ID             string         `json:"id"`
	Prompt         string         `json:"prompt"`
	DependsOn      []string       `json:"depends_on,omitempty"`
	Agent          string         `json:"agent"`
	Skill          string         `json:"skill,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	OutputSchema   map[string]any `json:"output_schema,omitempty"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

func (t *dispatchTasksTool) buildTasks(params []dispatchTaskParam, batchTimeout int) ([]subagents.Task, error) {
	tasks := make([]subagents.Task, len(params))
	batchID := fmt.Sprintf("dispatch:%d", t.nextBatch.Add(1))
	for i, pt := range params {
		route, err := resolveTaskRoute(t.agentReg, t.skillReg, pt.Agent, pt.Skill)
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
		providerName, model := resolvedTaskBinding(route, t.providerName, t.model)
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
			Name: route.agent.Name, AgentName: route.agent.Name, AgentDigest: route.digest,
			Skill: route.skill, Owner: defaultToolOwner,
			ProviderName: providerName, Model: model,
			Input: input, DependsOn: pt.DependsOn,
			Timeout:      time.Duration(taskTimeout) * time.Second,
			OutputSchema: outSchema, InputSchema: inSchema,
		}
	}
	return tasks, nil
}

// dispatchTaskResult is the per-task result envelope for dispatch_tasks model
// consumption. Fields added by the output-by-reference change (Synopsis,
// OutputBytes) use omitempty so they only appear when the result is above the
// inline threshold, preserving backward compatibility for small results.
type dispatchTaskResult struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	Output      any    `json:"output,omitempty"`
	OutputRef   string `json:"output_ref,omitempty"`
	OutputBytes int    `json:"output_bytes,omitempty"`
	Synopsis    string `json:"synopsis,omitempty"`
	ReadHint    string `json:"read_hint,omitempty"`
	ErrorRef    string `json:"error_ref,omitempty"`
	Error       string `json:"error,omitempty"`
	Steps       int    `json:"steps,omitempty"`
	Elapsed     string `json:"elapsed,omitempty"`
	StepCount   int64  `json:"step_count,omitempty"`
	// Schema is ok|violation when a schema was in force; omitted when none.
	Schema string `json:"schema,omitempty"`
	// Agent is the routed definition that produced this result. Parallel
	// research aggregates results from several agents, and without
	// provenance a caller cannot tell whose evidence it is holding.
	Agent string `json:"agent,omitempty"`
	// Reason is the typed termination cause. Status alone collapses
	// distinct outcomes - an operator cancel, a task deadline, an agent's
	// own ceiling, and a dependency that never ran all look alike - which
	// is exactly what a partially failed fan-out needs to distinguish.
	Reason string `json:"reason,omitempty"`
	// Messages are synopsis-only findings/questions posted during the task
	// (plan 53.02). Bodies stay behind content_ref via run_messages.
	Messages []messageSynopsis `json:"messages,omitempty"`
}

// messageSynopsis is the model-visible envelope entry for a task message.
type messageSynopsis struct {
	MessageID string `json:"message_id"`
	Kind      string `json:"kind"`
	Synopsis  string `json:"synopsis"`
}

func (t *dispatchTasksTool) encodeResults(tasks []ledger.TaskSnapshot, results []subagents.Result) string {
	threshold := t.cfg.InlineOutputBytes
	msgIndex := taskMessageIndex(context.Background(), t.repo, tasks)
	out := make([]dispatchTaskResult, len(results))
	for i, r := range results {
		out[i] = encodeOneDispatchResult(r, tasks, threshold)
		out[i].Messages = msgIndex[r.TaskID]
	}
	outJSON, _ := json.Marshal(out)
	return string(outJSON)
}

// encodeOneDispatchResult builds a single dispatchTaskResult from a subagent
// result, applying the inline-by-reference threshold for both output and error.
func encodeOneDispatchResult(r subagents.Result, tasks []ledger.TaskSnapshot, threshold int) dispatchTaskResult {
	tr := dispatchTaskResult{
		TaskID: r.TaskID,
		Status: r.Status,
		Agent:  agentForTask(tasks, r.TaskID),
		Reason: terminationReason(r),
	}
	// Only an unerrored result defaults to completed. Defaulting first and
	// unconditionally would label a failed task "completed" whenever the
	// subagent returned an error without setting Status, and would leave
	// setErrorFields' own failed-status fallback permanently dead.
	if tr.Status == "" && r.Err == nil {
		tr.Status = string(ledger.TaskStatusCompleted)
	}
	outputRef, errorRef := storedResultRefs(tasks, r)

	if r.Err != nil {
		setErrorFields(&tr, r.Err.Error(), r.Output, outputRef, errorRef, threshold)
		if tr.Reason == "schema_violation" && tr.Schema == "" {
			tr.Schema = "violation"
		}
	} else if len(r.Output) > 0 {
		setOutputFields(&tr, r.Output, outputRef, threshold)
		unpackElapsed(&tr, r.Output)
	}
	return tr
}

// taskMessageIndex loads synopsis-only findings/questions per task for result
// envelopes. Best-effort: a missing repo or events yields an empty map.
func taskMessageIndex(ctx context.Context, repo ledger.LedgerRepository, tasks []ledger.TaskSnapshot) map[string][]messageSynopsis {
	out := map[string][]messageSynopsis{}
	if repo == nil || len(tasks) == 0 {
		return out
	}
	runID := tasks[0].RunID
	if runID == "" {
		return out
	}
	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		return out
	}
	for _, e := range events {
		if e.Kind != coordinator.LifecycleKindTaskMessage {
			continue
		}
		var p struct {
			MessageID string `json:"message_id"`
			Kind      string `json:"kind"`
			Synopsis  string `json:"synopsis"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &p)
		}
		// Findings are the primary envelope attachment; questions appear too
		// so a parked/timeout path remains visible on the result.
		if p.Kind != "finding" && p.Kind != "question" {
			continue
		}
		out[e.TaskID] = append(out[e.TaskID], messageSynopsis{
			MessageID: p.MessageID, Kind: p.Kind, Synopsis: p.Synopsis,
		})
	}
	return out
}

// setOutputFields applies the inline-by-reference threshold to a result's
// output field, populating the dispatchTaskResult accordingly.
func setOutputFields(tr *dispatchTaskResult, output []byte, outputRef string, threshold int) {
	if belowInlineThreshold(output, threshold, outputRef) {
		tr.Output = modelVisibleOutput(output)
		if outputRef != "" {
			tr.OutputRef = outputRef
		}
	} else {
		tr.OutputRef = outputRef
		tr.OutputBytes = len(output)
		tr.Synopsis = synopsize(output)
		tr.ReadHint = readHint(threshold, len(output), outputRef)
	}
}

// setErrorFields applies the inline-by-reference threshold to both the error
// and (optionally) output fields of a failed task result.
func setErrorFields(tr *dispatchTaskResult, errMsg string, output []byte, outputRef, errorRef string, threshold int) {
	tr.ErrorRef = errorRef
	if belowInlineThreshold([]byte(errMsg), threshold, errorRef) {
		tr.Error = errMsg
	} else {
		tr.ErrorRef = errorRef
	}
	// Schema violations must not inline a known-malformed body; only the
	// envelope metadata and error_ref/path may surface.
	if tr.Reason == "schema_violation" || tr.Schema == "violation" {
		if len(output) > 0 {
			// Prefer ref path only; never put the body on tr.Output.
			if outputRef != "" {
				tr.OutputRef = outputRef
				tr.OutputBytes = len(output)
			}
			unpackElapsed(tr, output)
			if tr.Schema == "" {
				tr.Schema = "violation"
			}
		}
	} else if len(output) > 0 {
		setOutputFields(tr, output, outputRef, threshold)
	}
	if tr.Status == "" {
		tr.Status = string(ledger.TaskStatusFailed)
	}
}

// unpackElapsed extracts elapsed/steps/step_count/schema from structured JSON output.
func unpackElapsed(tr *dispatchTaskResult, output []byte) {
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		return
	}
	if s, ok := parsed["elapsed"].(string); ok {
		tr.Elapsed = s
	}
	if s, ok := parsed["steps"].(float64); ok {
		tr.Steps = int(s)
	}
	if s, ok := parsed["step_count"].(float64); ok {
		tr.StepCount = int64(s)
	}
	if s, ok := parsed["schema"].(string); ok {
		tr.Schema = s
	}
}

// agentForTask reports which routed definition owned a task. It reads the
// persisted routing snapshot rather than the request, so the answer is the
// definition the run was actually authorized against.
func agentForTask(tasks []ledger.TaskSnapshot, taskID string) string {
	for _, snap := range tasks {
		if snap.TaskID == taskID {
			return snap.AgentName
		}
	}
	return ""
}

// terminationReason classifies why a task stopped. It reports only a fixed
// vocabulary derived from the error's type, never the error text: this value
// is model-visible and aggregated across a fan-out, so it must not become a
// second channel for prompt or payload content.
func terminationReason(r subagents.Result) string {
	switch {
	case r.Status == "missing":
		return "never_started"
	case r.Err == nil:
		return ""
	case errors.Is(r.Err, subagents.ErrSchemaViolation):
		return "schema_violation"
	case errors.Is(r.Err, ErrAgentWallClockExceeded):
		return "agent_wall_clock_exceeded"
	case errors.Is(r.Err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(r.Err, context.Canceled):
		return "canceled"
	}
	// Fall back to the coarse status mapping, which already normalizes the
	// provider and transport error shapes this layer cannot type-assert on.
	switch statusFromErr(r.Err) {
	case string(ledger.TaskStatusTimedOut):
		return "deadline_exceeded"
	case string(ledger.TaskStatusCanceled):
		return "canceled"
	default:
		return "failed"
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
