package cliorchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// RunThroughCoordinator is the compatibility seam for legacy tools. It keeps
// delegate and dispatch_tasks on the same ledger, pool, cancellation, and
// identity path as the canonical orchestration tools.
func RunThroughCoordinator(ctx context.Context, d *runtime.Dispatcher, cfg config.SubagentConfig, tasks []subagents.Task, key string, repos ...ledger.LedgerRepository) (ledger.RunSnapshot, *coordinator.RunResult, error) {
	// Fail closed on an already-dead caller context: no work has been spawned,
	// so there is nothing to salvage (INV-AG-21 covers mid-run cancellation,
	// where the pool context is rooted in Background and finished work is read
	// back). Spawning now would run side-effecting subagents for a caller that
	// can no longer wait for or consume the result, and the tools would then
	// report completed work for a canceled call. The tools convert this error
	// into the structured cancel/timed_out envelope (StatusFromErr).
	if err := ctx.Err(); err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	caller, ok := runtime.CallerFrom(ctx)
	if !ok || caller.SessionID == "" {
		// Direct callers are supported for synchronous compatibility tools. They
		// receive an ephemeral principal, so they cannot later control a handle
		// without going through the dispatcher identity boundary.
		caller = runtime.Caller{SessionID: runtime.NewSessionID()}
		ctx = runtime.ContextWithCaller(ctx, caller)
	}
	principal, _ := principalFromContext(ctx)
	for i := range tasks {
		tasks[i].Depth = caller.Depth + 1
		tasks[i].SessionID = caller.SessionID
		tasks[i].TurnID = caller.TurnID
		tasks[i].Role = caller.Role
	}
	c := InitCoordinator(d, cfg, repos...)
	handle, isNew, err := c.SpawnNew(ctx, tasks, key)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	// Store the orchestration handle so that runs created by delegate and
	// dispatch_tasks are visible to join_run, cancel_run, and inspect_agents.
	snap, err := c.Inspect(ctx, handle)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	repo := EffectiveOrchestrationRepo(defaultOrchestrationRepo)
	if len(repos) > 0 {
		repo = EffectiveOrchestrationRepo(repos[0])
	}
	storeOrchestrationHandle(snap.RunID, &orchestrationHandle{
		coord: c, handle: handle, repo: repo, dispatcher: d, principal: principal, retention: orchestrationHandleRetention(cfg),
	})
	result, err := c.Join(ctx, handle)
	if err != nil {
		// The caller's context died before the run resolved. This is the
		// SYNCHRONOUS (wait=run) path - the caller is blocked waiting for
		// this exact run and has no other way to reach it - so treat the
		// caller's cancellation as an implicit request to stop the run too.
		// wait=none dispatches never reach this function: they return
		// immediately after Spawn and are joined later, independently, via
		// the join_run tool, so this cannot cancel a run the model
		// deliberately detached.
		//
		// isNew guards a narrower case the same reasoning misses: Spawn's
		// idempotency-key lookup can hand a DIFFERENT concurrent caller
		// (e.g. an async wait=none dispatch reusing the same
		// idempotency_key) this exact same *RunHandle. Canceling it then
		// would stop a run that caller is still relying on. Only cancel a
		// run this call actually created. Fire-and-forget: the run's own
		// graceful cancellation (the same path cancel_run uses) needs its
		// own context, since ctx is already dead, and
		// RunThroughCoordinator's caller has already given up waiting - it
		// must not block on this.
		if isNew {
			go cancelOrphanedRun(c, handle)
		}
		// Join returns ctx.Err() and never sets a result, so reporting the
		// error alone discards every task that had already finished - the
		// loss INV-AG-21 forbids. The work is in the ledger, so read it
		// back rather than throwing it away.
		if salvaged := salvageUnjoinedRun(c, handle, err); salvaged != nil {
			return salvaged.Snapshot, salvaged, nil
		}
		return ledger.RunSnapshot{}, nil, err
	}
	return result.Snapshot, result, nil
}

// cancelOrphanedRun propagates a synchronous dispatch's caller-cancellation
// into the run itself, through the same graceful path the cancel_run tool
// uses (coordinator.Cancel: records cancel_requested, cancels the run's
// pool context, and finalizes each task as canceled via CAS). Runs in its
// own goroutine with its own bounded, Background()-rooted context, since
// the caller's own context is already dead and RunThroughCoordinator has
// already returned to a caller that stopped waiting.
func cancelOrphanedRun(c coordinator.Coordinator, handle *coordinator.RunHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanedRunCancelTimeout)
	defer cancel()
	_ = c.Cancel(ctx, handle)
}

// orphanedRunCancelTimeout bounds cancelOrphanedRun's own wait for the
// run's graceful cancellation to settle. Generous: this runs detached from
// any caller, so there is no latency budget to protect - only a ceiling so
// a run stuck in a way even Cancel cannot resolve does not leak the
// goroutine forever.
const orphanedRunCancelTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// spawn and wait helpers
// ---------------------------------------------------------------------------

// spawnAndWait spawns tasks through the coordinator, stamps caller identity
// (depth/session/turn/role - the same stamping RunThroughCoordinator applies
// for the synchronous wait=run path, needed here too since not every task
// builder stamps it itself), registers the run handle so
// inspect_agents/join_run/cancel_run can find it, and waits per mode. Shared
// by spawn_agent and dispatch_tasks's async (wait != "run") path - there is
// exactly one place that spawns and waits, not two.
func spawnAndWait(ctx context.Context, d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository, tasks []subagents.Task, idempotencyKey, wait, waitTaskID string) (ledger.RunSnapshot, *coordinator.RunResult, error) {
	caller, _ := runtime.CallerFrom(ctx)
	for i := range tasks {
		tasks[i].Depth = caller.Depth + 1
		tasks[i].SessionID = caller.SessionID
		tasks[i].TurnID = caller.TurnID
		tasks[i].Role = caller.Role
	}
	principal, _ := principalFromContext(ctx)
	c := InitCoordinator(d, cfg, repo)
	handle, err := c.Spawn(ctx, tasks, idempotencyKey)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	snap, err := c.Inspect(ctx, handle)
	if err != nil {
		return ledger.RunSnapshot{}, nil, err
	}
	storeOrchestrationHandle(snap.RunID, &orchestrationHandle{
		coord: c, handle: handle, repo: EffectiveOrchestrationRepo(repo), dispatcher: d, principal: principal, retention: orchestrationHandleRetention(cfg),
	})
	return waitForSpawnResult(ctx, c, handle, wait, waitTaskID, snap)
}

func spawnResultPayload(snap ledger.RunSnapshot, completed *coordinator.RunResult, threshold int, repo ledger.LedgerRepository) string {
	result := map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
		"tasks":        taskSummaries(snap.Tasks),
	}
	// run_error mirrors join_run: spawn_agent wait=run and join_run are the same
	// operation reached two ways and must not disagree. Text, not a reference - a
	// run-level failure is never a task's recorded error, so nothing was stored
	// under its digest.
	//
	// This was dropped entirely, so every failure the DAG joins - a blocked
	// dependency on a non-partial run, retry exhaustion, a missing task result, any
	// ledger persistence error - reached the model as a payload that explained
	// nothing. It must not become a Go error instead: runtime.Dispatcher.failResult
	// replaces a failed tool's output with {"status":"failed"}, discarding both the
	// payload and the message.
	if completed != nil && completed.Err != nil {
		result["run_error"] = completed.Err.Error()
	}
	if completed != nil {
		// RunTaskResultsWithRepo, not ModelTaskResultsWithRepo: on the idempotent-replay
		// path the results are rebuilt from the ledger, so their references must come
		// off the snapshot rather than be minted from recovery prose.
		// repo attaches synopsis-only task messages (plan 53.02).
		result["task_results"] = RunTaskResultsWithRepo(repo, completed, threshold)
	}
	out, _ := json.Marshal(result)
	return string(out)
}

func normalizedSpawnWait(mode, taskID string) (string, error) {
	if mode == "" {
		mode = "none"
	}
	if mode != "none" && mode != "task" && mode != "run" {
		return "", fmt.Errorf("wait must be one of none, task, or run")
	}
	if mode == "task" && taskID == "" {
		return "", fmt.Errorf("wait_task_id is required when wait=task")
	}
	return mode, nil
}

// normalizedDispatchWait is normalizedSpawnWait with dispatch_tasks' default:
// an omitted wait blocks for the whole batch (mode "run"), matching
// dispatch_tasks' original always-synchronous contract, instead of
// spawn_agent's "none" default.
func normalizedDispatchWait(mode, taskID string) (string, error) {
	if mode == "" {
		mode = "run"
	}
	return normalizedSpawnWait(mode, taskID)
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

// ---------------------------------------------------------------------------
// inspect_agent
// ---------------------------------------------------------------------------

type inspectAgentTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *inspectAgentTool) Name() string { return ToolInspectAgents }
func (t *inspectAgentTool) Privileged()  {}

func (t *inspectAgentTool) Description() string {
	return "Inspect a previously dispatched orchestration run. " +
		"Returns the current run snapshot including status, task states, " +
		"timestamps, and any output/error references. " +
		"Use after dispatch_tasks (with wait=\"none\" or wait=\"task\") to check progress or after join_run to see final state."
}

func (t *inspectAgentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID returned by dispatch_tasks",
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
	record, errJSON := accessibleOrchestrationHandle(ctx, params.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
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
		// DependsOn entries are themselves real (possibly namespaced)
		// sibling task ids from this same run - resolve each through the
		// run's own task list rather than the string-based stripNamespace,
		// the same way TaskID itself resolves via modelVisibleTaskID.
		dependsOn := make([]string, len(t.DependsOn))
		for j, dep := range t.DependsOn {
			dependsOn[j] = taskRawIDByID(snap.Tasks, dep)
		}
		tasks[i] = taskInfo{
			TaskID:      modelVisibleTaskID(t),
			DisplayName: t.DisplayName,
			Status:      t.Status,
			DependsOn:   dependsOn,
			OutputRef:   t.OutputRef,
			ErrorRef:    t.ErrorRef,
		}
	}

	// Surface live parked questions so the model can see askers blocked on an
	// answer without polling the whole run (empty list when none).
	parks := record.coord.ParkedQuestions(snap.RunID)
	if parks == nil {
		parks = []coordinator.ParkedQuestion{}
	}

	out, _ := json.Marshal(map[string]any{
		"run_id":       snap.RunID,
		"display_name": snap.DisplayName,
		"status":       snap.Status,
		"tasks":        tasks,
		"parks":        parks,
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
		case <-time.After(orchestrationPollInterval):
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

// RegisterOrchestrationTools registers the orchestration tools (dispatch_tasks,
// inspect_agent, join_run, cancel_run) on both the model-visible
// registry and the runtime dispatcher. It is called from NewSessionDispatcher.
// dispatch_tasks is registered first: session_tool_catalog.go documents that
// the resulting wire order is load-cache-stability-sensitive for
// OpenAI-compatible providers, so this order must not change.
func RegisterOrchestrationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, repo ledger.LedgerRepository, skillReg *skills.Registry, agentReg *agents.AgentRegistry, providerName, model string) error {
	toolSet := []tools.Tool{
		&dispatchTasksTool{dispatcher: d, cfg: cfg, skillReg: skillReg, repo: repo, agentReg: agentReg, providerName: providerName, model: model},
		&inspectAgentTool{dispatcher: d, cfg: cfg, repo: repo},
		&joinRunTool{dispatcher: d, cfg: cfg, repo: repo},
		&cancelRunTool{dispatcher: d, cfg: cfg, repo: repo},
	}
	for _, t := range toolSet {
		if err := cliagents.RegisterSessionTool(d, reg, t); err != nil {
			return err
		}
	}
	return nil
}
