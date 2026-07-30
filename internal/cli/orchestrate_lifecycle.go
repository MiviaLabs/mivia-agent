package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type modelTaskResult struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Output    any    `json:"output,omitempty"`
	OutputRef string `json:"output_ref,omitempty"`
	Error     string `json:"error,omitempty"`
	ErrorRef  string `json:"error_ref,omitempty"`
}

// modelTaskResults returns live orchestration results for model consumption.
// The output is included inline here while the completed run is in memory.
//
// References come from the task records via storedResultRefs rather than being
// re-minted from the in-memory bytes. The two agree on every successful run, but
// when a content write fails the coordinator deliberately records no reference —
// and re-minting here would hand the model a digest nothing was stored under
// (INV-AG-10). Reading the recorded value is what makes the reference honest.
func modelTaskResults(tasks []ledger.TaskSnapshot, results []subagents.Result) []modelTaskResult {
	out := make([]modelTaskResult, len(results))
	for i, result := range results {
		out[i] = modelTaskResult{TaskID: result.TaskID, Status: result.Status}
		if out[i].Status == "" {
			out[i].Status = "completed"
		}
		outputRef, errorRef := storedResultRefs(tasks, result)
		if len(result.Output) > 0 {
			out[i].Output = modelVisibleOutput(result.Output)
			out[i].OutputRef = outputRef
		}
		if result.Err != nil {
			out[i].Error = result.Err.Error()
			out[i].ErrorRef = errorRef
		}
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
			TaskID: task.TaskID, Status: task.Status,
			OutputRef: task.OutputRef, ErrorRef: task.ErrorRef,
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

// runTaskResults returns the model-visible task results for a completed run,
// preferring the snapshot's stored references on the recovered/replay path.
func runTaskResults(result *coordinator.RunResult) []modelTaskResult {
	if result == nil {
		return nil
	}
	if allResultsRecovered(result) {
		return persistedTaskResults(result.Snapshot.Tasks)
	}
	return modelTaskResults(result.Snapshot.Tasks, result.Results)
}

// storedResultRefs returns the references the ledger recorded for a result's
// task. It falls back to canonical minting only when the snapshot carries no
// record for that task at all.
func storedResultRefs(tasks []ledger.TaskSnapshot, r subagents.Result) (outputRef, errorRef string) {
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

func (t *joinRunTool) Name() string { return "join_run" }
func (t *joinRunTool) Privileged()  {}

func (t *joinRunTool) Description() string {
	return "Join (block until) a previously spawned orchestration run completes. " +
		"Returns the final live run result including per-task structured output, status, " +
		"correlation references, and any errors. Recovered historical runs expose references only. Blocks until the run finishes or the " +
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
	if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
		return `{"error":"unknown run_id"}`, nil
	}
	handle := record.handle

	result, err := record.coord.Join(ctx, handle)
	if err != nil {
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
		"task_results": runTaskResults(result),
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
	repo       ledger.LedgerRepository
}

func (t *cancelRunTool) Name() string { return "cancel_run" }
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
				"description": "Run ID returned by spawn_agent",
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
	if params.RunID == "" {
		return `{"error":"run_id is required"}`, nil
	}

	rawHandle, ok := runHandles.Load(params.RunID)
	if !ok {
		return `{"error":"unknown run_id"}`, nil
	}
	record, ok := rawHandle.(*orchestrationHandle)
	if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
		return `{"error":"unknown run_id"}`, nil
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

func (t *cancelRunTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:   tools.ExecutionWrite,
		Timeout: time.Duration(config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)) * time.Second,
	}
}
