package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// dispatchTaskResult is the per-task result envelope for dispatch_tasks model
// consumption. Fields added by the output-by-reference change (Synopsis,
// OutputBytes) use omitempty so they only appear when the result is above the
// inline threshold, preserving backward compatibility for small results.
//
// This duplicates orchestrate_lifecycle.go's modelTaskResult (spawn_agent's
// async envelope) minus Steps/Elapsed/StepCount/Schema/Agent/Reason, which
// modelTaskResult does not carry today - unifying the two structs is tracked
// as follow-up cleanup, not done here to avoid silently dropping those
// fields from dispatch_tasks' richer output.
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
	// Messages are synopsis-only findings/questions posted during the task.
	// Bodies stay behind content_ref via run_messages.
	Messages []messageSynopsis `json:"messages,omitempty"`
}

func (t *dispatchTasksTool) encodeResults(tasks []ledger.TaskSnapshot, results []subagents.Result) string {
	threshold := t.cfg.InlineOutputBytes
	msgIndex := TaskMessageIndex(context.Background(), t.repo, tasks)
	out := make([]dispatchTaskResult, len(results))
	for i, r := range results {
		out[i] = EncodeOneDispatchResult(r, tasks, threshold)
		out[i].Messages = msgIndex[r.TaskID]
	}
	outJSON, _ := json.Marshal(out)
	return string(outJSON)
}

// EncodeOneDispatchResult builds a single dispatchTaskResult from a subagent
// result, applying the inline-by-reference threshold for both output and error.
func EncodeOneDispatchResult(r subagents.Result, tasks []ledger.TaskSnapshot, threshold int) dispatchTaskResult {
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
	outputRef, errorRef := StoredResultRefs(tasks, r)

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

// TaskMessageIndex loads synopsis-only findings/questions per task for result
// envelopes. Best-effort: a missing repo or events yields an empty map.
func TaskMessageIndex(ctx context.Context, repo ledger.LedgerRepository, tasks []ledger.TaskSnapshot) map[string][]messageSynopsis {
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
	if BelowInlineThreshold(output, threshold, outputRef) {
		tr.Output = ModelVisibleOutput(output)
		if outputRef != "" {
			tr.OutputRef = outputRef
		}
	} else {
		tr.OutputRef = outputRef
		tr.OutputBytes = len(output)
		tr.Synopsis = Synopsize(output)
		tr.ReadHint = ReadHint(threshold, len(output), outputRef)
	}
}

// setErrorFields applies the inline-by-reference threshold to both the error
// and (optionally) output fields of a failed task result.
func setErrorFields(tr *dispatchTaskResult, errMsg string, output []byte, outputRef, errorRef string, threshold int) {
	tr.ErrorRef = errorRef
	if BelowInlineThreshold([]byte(errMsg), threshold, errorRef) {
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
	case errors.Is(r.Err, cliagents.ErrAgentWallClockExceeded):
		return "agent_wall_clock_exceeded"
	case errors.Is(r.Err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(r.Err, context.Canceled):
		return "canceled"
	}
	// Fall back to the coarse status mapping, which already normalizes the
	// provider and transport error shapes this layer cannot type-assert on.
	switch StatusFromErr(r.Err) {
	case string(ledger.TaskStatusTimedOut):
		return "deadline_exceeded"
	case string(ledger.TaskStatusCanceled):
		return "canceled"
	default:
		return "failed"
	}
}
