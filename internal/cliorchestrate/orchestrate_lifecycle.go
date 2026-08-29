package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// modelTaskResult is the per-task result envelope for model consumption.
// Fields added by the output-by-reference change (Synopsis, OutputBytes)
// use omitempty so they only appear when the result is above the inline
// threshold, preserving backward compatibility for small results.
type modelTaskResult struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	Output      any    `json:"output,omitempty"`
	OutputRef   string `json:"output_ref,omitempty"`
	OutputBytes int    `json:"output_bytes,omitempty"`
	Synopsis    string `json:"synopsis,omitempty"`
	ReadHint    string `json:"read_hint,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorRef    string `json:"error_ref,omitempty"`
	// Messages are synopsis-only findings/questions posted during the task.
	Messages []messageSynopsis `json:"messages,omitempty"`
	// ToolCallsRef is the resolvable reference to the task's recorded
	// tool-call trace; same contract as dispatchTaskResult.ToolCallsRef
	// (read from the task record, never re-minted; page via ledger_read).
	ToolCallsRef string `json:"tool_calls_ref,omitempty"`
}

// ModelTaskResults returns live orchestration results for model consumption.
// The output is included inline here while the completed run is in memory.
//
// References come from the task records via StoredResultRefs rather than being
// re-minted from the in-memory bytes. The two agree on every successful run, but
// when a content write fails the coordinator deliberately records no reference -
// and re-minting here would hand the model a digest nothing was stored under
// (INV-AG-10). Reading the recorded value is what makes the reference honest.
//
// threshold controls the inline-by-reference switch: results whose output body
// is at or below threshold bytes are inlined; above threshold, only ref+synopsis
// are emitted. When no ref is available (content write failed), the body is
// always inlined regardless of size.
func ModelTaskResults(tasks []ledger.TaskSnapshot, results []subagents.Result, threshold int) []modelTaskResult {
	return ModelTaskResultsWithRepo(nil, tasks, results, threshold)
}

// ModelTaskResultsWithRepo is like ModelTaskResults but attaches synopsis-only
// messages from the run ledger when repo is non-nil.
func ModelTaskResultsWithRepo(repo ledger.LedgerRepository, tasks []ledger.TaskSnapshot, results []subagents.Result, threshold int) []modelTaskResult {
	msgIndex := TaskMessageIndex(context.Background(), repo, tasks)
	out := make([]modelTaskResult, len(results))
	for i, result := range results {
		out[i] = modelTaskResult{TaskID: taskRawIDByID(tasks, result.TaskID), Status: result.Status}
		if out[i].Status == "" {
			out[i].Status = "completed"
		}
		outputRef, errorRef := StoredResultRefs(tasks, result)
		if len(result.Output) > 0 {
			if BelowInlineThreshold(result.Output, threshold, outputRef) {
				out[i].Output = ModelVisibleOutput(result.Output)
				if outputRef != "" {
					out[i].OutputRef = outputRef
				}
			} else {
				out[i].OutputRef = outputRef
				out[i].OutputBytes = len(result.Output)
				out[i].Synopsis = Synopsize(result.Output)
				hint := ReadHint(threshold, len(result.Output), outputRef)
				if hint != "" {
					out[i].ReadHint = hint
				}
			}
		}
		if result.Err != nil {
			if BelowInlineThreshold([]byte(result.Err.Error()), threshold, errorRef) {
				out[i].Error = result.Err.Error()
				if errorRef != "" {
					out[i].ErrorRef = errorRef
				}
			} else {
				out[i].ErrorRef = errorRef
			}
		}
		out[i].Messages = msgIndex[result.TaskID]
		// A pure snapshot read: no repo needed, so the nil-repo entry point
		// (ModelTaskResults) emits the same reference dialect.
		out[i].ToolCallsRef = toolCallsRefFor(tasks, result.TaskID)
	}
	return out
}

// persistedTaskResults builds model results straight from the ledger's task
// records. Recovered results carry no Output and their Err is prose about the
// recovery, so minting references from those values would hand the model a
// digest nothing was stored under; the snapshot holds the real keys.
func persistedTaskResults(tasks []ledger.TaskSnapshot) []modelTaskResult {
	out := make([]modelTaskResult, len(tasks))
	for i, task := range tasks {
		out[i] = modelTaskResult{
			TaskID: modelVisibleTaskID(task), Status: task.Status,
			OutputRef: task.OutputRef, ErrorRef: task.ErrorRef,
			// Read off the snapshot row directly: the model-visible TaskID
			// above is the namespace-stripped RawID, so any later lookup
			// keyed on it (toolCallsRefFor matches the full TaskID) misses
			// dispatch-namespaced tasks.
			ToolCallsRef: task.ToolCallsRef,
		}
	}
	return out
}

// allResultsRecovered reports whether every result of a completed run was
// rebuilt from the ledger rather than produced by a live execution.
func allResultsRecovered(result *coordinator.RunResult) bool {
	if result == nil || len(result.Snapshot.Tasks) == 0 {
		return false
	}
	if len(result.Results) != len(result.Snapshot.Tasks) {
		return false
	}
	for _, r := range result.Results {
		if r.Provenance.Kind != "recovered" {
			return false
		}
	}
	return true
}

// RunTaskResults returns the model-visible task results for a completed run,
// preferring the snapshot's stored references on the recovered/replay path.
// When repo is non-nil, synopsis-only task messages are attached (plan 53.02).
func RunTaskResults(result *coordinator.RunResult, threshold int) []modelTaskResult {
	return RunTaskResultsWithRepo(nil, result, threshold)
}

// RunTaskResultsWithRepo is like RunTaskResults but attaches synopsis-only
// task messages when repo is non-nil.
func RunTaskResultsWithRepo(repo ledger.LedgerRepository, result *coordinator.RunResult, threshold int) []modelTaskResult {
	if result == nil {
		return nil
	}
	if allResultsRecovered(result) {
		out := persistedTaskResults(result.Snapshot.Tasks)
		if repo != nil {
			msgIndex := TaskMessageIndex(context.Background(), repo, result.Snapshot.Tasks)
			for i := range out {
				// Key by the snapshot row's FULL TaskID (rows are
				// index-aligned with the snapshot): the coordinator records
				// task-message events under the namespaced id, while
				// out[i].TaskID is the stripped model-visible RawID, which
				// misses every dispatch_tasks-created task.
				out[i].Messages = msgIndex[result.Snapshot.Tasks[i].TaskID]
			}
		}
		return out
	}
	return ModelTaskResultsWithRepo(repo, result.Snapshot.Tasks, result.Results, threshold)
}

// StoredResultRefs returns the references the ledger recorded for a result's
// task. It falls back to canonical minting only when the snapshot carries no
// record for that task at all.
func StoredResultRefs(tasks []ledger.TaskSnapshot, r subagents.Result) (outputRef, errorRef string) {
	for _, task := range tasks {
		if task.TaskID == r.TaskID {
			return task.OutputRef, task.ErrorRef
		}
	}
	if len(r.Output) > 0 {
		outputRef = ledger.Reference(ledger.RefKindOutput, r.Output)
	}
	if r.Err != nil {
		errorRef = ledger.Reference(ledger.RefKindError, []byte(r.Err.Error()))
	}
	return outputRef, errorRef
}

// ---------------------------------------------------------------------------
// join_run
// ---------------------------------------------------------------------------

type joinRunTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *joinRunTool) Name() string { return ToolJoinRun }
func (t *joinRunTool) Privileged()  {}

func (t *joinRunTool) Description() string {
	return "Join (block until) a previously spawned orchestration run completes. " +
		"Returns the final live run result including per-task structured output, status, " +
		"correlation references, and any errors. For large task results, output_ref is returned instead of inline output; use ledger_read to fetch the full body. " +
		"Each task's recorded tool activity is behind tool_calls_ref; use ledger_read to page it on demand. " +
		"Recovered historical runs expose references only. Blocks until the run finishes, " +
		"timeout_seconds elapses (default " + joinDefaultSeconds(t.cfg) + "s, cap 3600), or the calling context is canceled. " +
		"On join timeout the run is gracefully canceled and the response says so - never retry a wedged join blindly."
}

func (t *joinRunTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID returned by dispatch_tasks",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Max seconds to block waiting for the run. Default " + joinDefaultSeconds(t.cfg) + ", hard cap 3600. On expiry the run is gracefully canceled and a join_timeout envelope is returned.",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}

// joinFallbackSeconds is the join budget when nothing resolves: no
// configured default and no request. joinDefaultSeconds (what the schema
// advertises) and joinTimeout (what enforcement applies) must agree on it,
// so both read this one constant.
const joinFallbackSeconds = 600

// joinDefaultSeconds reports the model-visible default join budget:
// the effective timeout joinTimeout would use when no timeout_seconds is
// passed (0 -> joinFallbackSeconds), clamped to the same 3600 cap so the
// advertised default never exceeds the enforced cap.
func joinDefaultSeconds(cfg config.SubagentConfig) string {
	if eff := joinBudget(cfg, 0); eff > 0 {
		return fmt.Sprint(eff)
	}
	return fmt.Sprint(joinFallbackSeconds)
}

// joinBudget resolves the effective join seconds: config default, then the
// caller's request, hard-capped at 3600. An over-cap request clamps to the
// cap - it must never fall back to the 600 default (which would silently
// truncate a deliberately long join and then cancel a healthy run).
func joinBudget(cfg config.SubagentConfig, requested int) int {
	eff := 0
	if cfg.DefaultTimeout > 0 {
		eff = cfg.DefaultTimeout
	}
	if requested > 0 {
		eff = requested
	}
	if eff > 3600 {
		eff = 3600
	}
	return eff
}

// joinTimeout bounds one join call (BUG-D fix): coordinator.Join blocks
// purely on h.done or caller-ctx death, so a WEDGED run (task stuck running
// past its declared budget - e.g. an upstream HTTP call that ignores its
// context) used to trap join_run callers until their whole turn died with no
// diagnostic and no cleanup. The wrapper guarantees escape; on expiry we
// propagate graceful cancellation through the same path cancel_run uses so
// the wedged run finalizes instead of leaking.
func joinTimeout(ctx context.Context, cfg config.SubagentConfig, requested int) (context.Context, context.CancelFunc) {
	eff := joinBudget(cfg, requested)
	if eff <= 0 {
		eff = joinFallbackSeconds
	}
	return context.WithTimeout(ctx, time.Duration(eff)*time.Second)
}

func (t *joinRunTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		RunID          string `json:"run_id"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("join_run: %w", err)
	}
	record, errJSON := accessibleOrchestrationHandle(ctx, params.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	handle := record.handle

	joinCtx, cancel := joinTimeout(ctx, t.cfg, params.TimeoutSeconds)
	defer cancel()
	result, err := record.coord.Join(joinCtx, handle)
	if err != nil {
		// BUG-D: never leave the caller trapped on a run that may be wedged,
		// and never discard work that already finished (same INV-AG-21 shape
		// as RunThroughCoordinator): salvage whatever task records exist.
		if salvaged := salvageUnjoinedRun(record.coord, handle, err); salvaged != nil {
			return joinSalvageEnvelope(record.coord, handle, salvaged, err, ctx.Err() == nil), nil
		}
		snap := latestSnapshot(record.coord, handle, ctx)
		status := "unknown"
		if snap.Status != "" {
			status = string(snap.Status)
		}
		if errors.Is(err, context.DeadlineExceeded) || (errors.Is(err, context.Canceled) && ctx.Err() == nil) {
			// Join timed out but the caller is alive: fire-and-forget graceful
			// cancel of the likely-wedged run (own Background context - the
			// pattern is cancelOrphanedRun's), then answer with the run_id so
			// the caller can inspect/cancel/join again later.
			go cancelWedgedRun(record.coord, handle)
			payload, _ := json.Marshal(map[string]string{
				"error":  "join_timeout: run did not finish within the join budget; graceful cancel dispatched",
				"status": status,
				"run_id": params.RunID,
				"hint":   "inspect_agents later for final state; re-join with a larger timeout_seconds if desired",
			})
			return string(payload), nil
		}
		return "", fmt.Errorf("join_run: %w", err)
	}

	// run_error carries the error text, not a reference: a run-level failure is
	// never a task's recorded error, so nothing was stored under its digest.
	runErr := ""
	if result.Err != nil {
		runErr = result.Err.Error()
	}

	out, _ := json.Marshal(map[string]any{
		"run_id":       result.Snapshot.RunID,
		"display_name": result.Snapshot.DisplayName,
		"status":       result.Snapshot.Status,
		"run_error":    runErr,
		"task_results": RunTaskResultsWithRepo(t.repo, result, t.cfg.InlineOutputBytes),
	})
	return string(out), nil
}

// Ensure joinRunTool implements required interfaces at compile time.
var _ tools.Tool = (*joinRunTool)(nil)

func (t *joinRunTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionExternal,
		Timeout: defaultJoinRunTimeout, // long-running wait
	}
}

// ---------------------------------------------------------------------------
// cancel_run
// ---------------------------------------------------------------------------

type cancelRunTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *cancelRunTool) Name() string { return ToolCancelRun }
func (t *cancelRunTool) Privileged()  {}

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
				"description": "Run ID returned by dispatch_tasks",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}

func (t *cancelRunTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}
	record, errJSON := accessibleOrchestrationHandle(ctx, params.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	handle := record.handle

	if err := record.coord.Cancel(ctx, handle); err != nil {
		return "", fmt.Errorf("cancel_run: %w", err)
	}

	snap, err := record.coord.Inspect(ctx, handle)
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

// cancelWedgedRun propagates a join-timeout into the likely-wedged run via
// the same graceful path cancel_run uses (Cancel: records cancel_requested,
// cancels the run pool, CAS-finalizes each task). Runs detached with its own
// bounded Background context - the join caller must not block on it - with
// orphanedRunCancelTimeout as the ceiling so even an unresponsive Cancel
// cannot leak the goroutine forever.
func cancelWedgedRun(c coordinator.Coordinator, h *coordinator.RunHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanedRunCancelTimeout)
	defer cancel()
	_ = c.Cancel(ctx, h)
}

// latestSnapshot is a best-effort status read for error envelopes. A wedged
// or canceled run may not answer Inspect within the short budget; callers
// treat the zero snapshot as "status unknown" and say so instead of
// guessing.
func latestSnapshot(c coordinator.Coordinator, h *coordinator.RunHandle, ctx context.Context) ledger.RunSnapshot {
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	snap, err := c.Inspect(qctx, h)
	if err != nil {
		return ledger.RunSnapshot{}
	}
	return snap
}

// salvageErrorText labels partial results returned by salvageUnjoinedRun on
// a failed/timeout join: everything below IS real recorded work, but it is
// not a clean completion and the caller should know why the envelope exists.
func salvageErrorText(err error) string {
	if err == nil {
		return ""
	}
	return "joined after failure: " + err.Error()
}

// joinSalvageHint tells a salvage-envelope reader what happens to the run
// next: the two join failures have opposite continuations. Without it, a
// caller canceled mid-join reads "joined after failure: context canceled"
// plus a partial task list with no idea the run is still live and
// re-joinable - the exact "stuck" confusion this envelope exists to prevent.
func joinSalvageHint(joinBudgetExpired bool) string {
	if joinBudgetExpired {
		return "join budget expired; graceful cancel dispatched - inspect_agents later for final state"
	}
	return "the join was cut by the caller's own cancel; the run keeps running under its own budget - inspect_agents, re-join, or cancel_run"
}

// joinSalvageEnvelope renders the partial-results envelope for a join cut
// before completion, and decides the run's continuation with it. A
// join-budget expiration with the caller still alive dispatches the graceful
// cancel: salvage returning finished work must not preempt the wedged-run
// guarantee (the early return once made the timeout branch unreachable for
// every run worth salvaging, leaking a wedged task to its full budget). A
// caller cancel leaves the run running under its own budget - another caller
// may own it, and walking away is not a verdict. Deriving joinBudgetExpired
// here keeps the hint and the cancel decision from ever disagreeing.
func joinSalvageEnvelope(coord coordinator.Coordinator, handle *coordinator.RunHandle, salvaged *coordinator.RunResult, joinErr error, callerAlive bool) string {
	joinBudgetExpired := errors.Is(joinErr, context.DeadlineExceeded) && callerAlive
	if joinBudgetExpired {
		go cancelWedgedRun(coord, handle)
	}
	out, _ := json.Marshal(map[string]any{
		"run_id":       salvaged.Snapshot.RunID,
		"display_name": salvaged.Snapshot.DisplayName,
		"status":       salvaged.Snapshot.Status,
		"run_error":    salvageErrorText(joinErr),
		"task_results": persistedTaskResults(salvaged.Snapshot.Tasks),
		"hint":         joinSalvageHint(joinBudgetExpired),
	})
	return string(out)
}

func (t *cancelRunTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionWrite,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}
