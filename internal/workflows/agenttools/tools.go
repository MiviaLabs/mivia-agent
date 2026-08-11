package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool is the agent-facing surface for one workflow operation.
// Implementations live here so package tools can wrap them without cycles
// beyond the Service dependency.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	ResultBudgetBytes() int
	// Class is "read" or "write" for capability scheduling.
	Class() string
}

// Tools returns the eight workflow tools bound to svc.
func Tools(svc *Service) []Tool {
	if svc == nil {
		return nil
	}
	return []Tool{
		&runTool{svc: svc},
		&statusTool{svc: svc},
		&eventsTool{svc: svc},
		&inspectTool{svc: svc},
		&listRunsTool{svc: svc},
		&deliverTool{svc: svc},
		&cancelTool{svc: svc},
		&deleteTool{svc: svc},
	}
}

// ---------------------------------------------------------------------------
// workflow_run
// ---------------------------------------------------------------------------

type runTool struct{ svc *Service }

func (t *runTool) Name() string           { return ToolWorkflowRun }
func (t *runTool) Class() string          { return "write" }
func (t *runTool) ResultBudgetBytes() int { return DefaultRunBudgetBytes }
func (t *runTool) Description() string {
	return "Admit and start a named workflow from the workspace workflow directory. " +
		"Returns a durable run_id immediately; the controller advances in the background. " +
		"Pass resume=true with run_id to resume an interrupted run from the durable snapshot. " +
		"A delivery-capable workflow (one with an active [delivery] policy) is published " +
		"automatically by the harness: the workflow's policy is the publication grant, so no " +
		"allow_publish flag is needed (the explicit workflow_deliver tool keeps allow_publish as " +
		"its gate). " +
		"Available to agents by default when the workspace defines workflows; use it when a workflow fits the task."
}
func (t *runTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workflow": map[string]any{
				"type":        "string",
				"description": "Workflow name as discovered under the workspace workflow directory (required unless resume=true)",
			},
			"inputs": map[string]any{
				"type":                 "object",
				"description":          "Validated input map for the workflow (name to value); required keys come from the workflow definition",
				"additionalProperties": true,
			},
			"invocation_key": map[string]any{
				"type":        "string",
				"description": "Stable caller key for retrying the same workflow request without creating a second run",
			},
			"allow_publish": map[string]any{
				"type":        "boolean",
				"description": "Accepted for compatibility; the harness publishes a delivery-capable workflow automatically (the workflow's [delivery] policy is the grant). The explicit workflow_deliver tool uses allow_publish as its gate",
			},
			"resume": map[string]any{
				"type":        "boolean",
				"description": "When true, resume run_id from the durable ledger snapshot instead of admitting a new run",
			},
			"run_id": map[string]any{
				"type":        "string",
				"description": "Existing run id (form wfr-...); required when resume=true",
			},
			"force": map[string]any{
				"type":        "boolean",
				"description": "When resuming, clear a stale execution claim first; use only after the prior executor stopped",
			},
		},
		"additionalProperties": false,
	}
}
func (t *runTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Workflow      string         `json:"workflow"`
		Inputs        map[string]any `json:"inputs"`
		InvocationKey string         `json:"invocation_key"`
		AllowPublish  bool           `json:"allow_publish"`
		Resume        bool           `json:"resume"`
		RunID         string         `json:"run_id"`
		Force         bool           `json:"force"`
	}
	if len(args) > 0 && string(args) != "null" {
		// Decode with UseNumber so integer inputs ≥ 2^53 stay exact: the plain
		// float64 decode rounds them, and the admitted run would execute with
		// different input than requested (silent corruption). json.Number
		// re-marshals verbatim downstream.
		dec := json.NewDecoder(bytes.NewReader(args))
		dec.UseNumber()
		if err := dec.Decode(&in); err != nil {
			return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowRun, err)
		}
	}
	result, err := t.svc.Run(ctx, StartRequest{
		Workflow:      strings.TrimSpace(in.Workflow),
		Inputs:        in.Inputs,
		InvocationKey: strings.TrimSpace(in.InvocationKey),
		AllowPublish:  in.AllowPublish,
		Resume:        in.Resume,
		RunID:         strings.TrimSpace(in.RunID),
		Force:         in.Force,
	})
	if err != nil {
		return "", err
	}
	return encodeJSON(result, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_status
// ---------------------------------------------------------------------------

type statusTool struct{ svc *Service }

func (t *statusTool) Name() string           { return ToolWorkflowStatus }
func (t *statusTool) Class() string          { return "read" }
func (t *statusTool) ResultBudgetBytes() int { return t.svc.budget("status") }
func (t *statusTool) Description() string {
	return "Report deep status for one workflow run from the durable ledger: " +
		"state, active step, numbered attempts (output digests, routes, gate verdicts), " +
		"loop counters, approvals, and delivery records. Read-only; does not mutate run state. " +
		"Available to agents by default when the workspace defines workflows; use it to observe a run."
}
func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id (form wfr-...)",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}
func (t *statusTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowStatus, err)
	}
	view, err := t.svc.Status(ctx, strings.TrimSpace(in.RunID))
	if err != nil {
		return "", err
	}
	return encodeJSON(view, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_events
// ---------------------------------------------------------------------------

type eventsTool struct{ svc *Service }

func (t *eventsTool) Name() string           { return ToolWorkflowEvents }
func (t *eventsTool) Class() string          { return "read" }
func (t *eventsTool) ResultBudgetBytes() int { return t.svc.budget("events") }
func (t *eventsTool) Description() string {
	return "Return a paged, ordered audit trail for one workflow run: sequence, timestamp, " +
		"kind, and a bounded detail summary. Summaries never include raw prompts or credentials. " +
		"Read-only; does not mutate run state. " +
		"Available to agents by default when the workspace defines workflows; use it to audit a run."
}
func (t *eventsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id (form wfr-...)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Maximum events to return (default 50); 0 uses the default",
			},
			"offset": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Number of events to skip from the start of the trail",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}
func (t *eventsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID  string `json:"run_id"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowEvents, err)
	}
	page, err := t.svc.Events(ctx, strings.TrimSpace(in.RunID), in.Limit, in.Offset)
	if err != nil {
		return "", err
	}
	return encodeJSON(page, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_inspect
// ---------------------------------------------------------------------------

type inspectTool struct{ svc *Service }

func (t *inspectTool) Name() string           { return ToolWorkflowInspect }
func (t *inspectTool) Class() string          { return "read" }
func (t *inspectTool) ResultBudgetBytes() int { return t.svc.budget("inspect") }
func (t *inspectTool) Description() string {
	return "Inspect one workflow step attempt: validated output JSON, evidence selection, " +
		"transition decision, and coordinator run/task references for tool-call tracing. " +
		"Large output artifacts are paged; offset and limit select the page of output text. " +
		"Read-only; does not mutate run state. " +
		"Available to agents by default when the workspace defines workflows; use it to trace a step."
}
func (t *inspectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id (form wfr-...)",
			},
			"step": map[string]any{
				"type":        "string",
				"description": "Step id from the workflow definition",
			},
			"attempt": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Attempt number for that step (starts at 1)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Maximum output bytes to return in one page (default 0); 0 uses the service default page size",
			},
			"offset": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Number of output bytes to skip from the start of the artifact (default 0)",
			},
		},
		"required":             []string{"run_id", "step", "attempt"},
		"additionalProperties": false,
	}
}
func (t *inspectTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID   string `json:"run_id"`
		Step    string `json:"step"`
		Attempt int    `json:"attempt"`
		Limit   int    `json:"limit"`
		Offset  int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowInspect, err)
	}
	view, err := t.svc.Inspect(ctx, strings.TrimSpace(in.RunID), strings.TrimSpace(in.Step), in.Attempt, in.Offset, in.Limit)
	if err != nil {
		return "", err
	}
	return encodeJSON(view, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_list_runs
// ---------------------------------------------------------------------------

type listRunsTool struct{ svc *Service }

func (t *listRunsTool) Name() string           { return ToolWorkflowListRuns }
func (t *listRunsTool) Class() string          { return "read" }
func (t *listRunsTool) ResultBudgetBytes() int { return t.svc.budget("list") }
func (t *listRunsTool) Description() string {
	return "List active and historical workflow runs with state, workflow name, and age. " +
		"Optional status filter and paging. Read-only; does not mutate run state. " +
		"Available to agents by default when the workspace defines workflows; use it to find a run."
}
func (t *listRunsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{
				"type":        "string",
				"description": "Optional run status filter (for example running, succeeded, canceled, delivery_pending)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Maximum runs to return (default 50); 0 uses the default",
			},
			"offset": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Number of runs to skip",
			},
		},
		"additionalProperties": false,
	}
}
func (t *listRunsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Status string `json:"status"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowListRuns, err)
		}
	}
	page, err := t.svc.ListRuns(ctx, strings.TrimSpace(in.Status), in.Limit, in.Offset)
	if err != nil {
		return "", err
	}
	return encodeJSON(page, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_deliver
// ---------------------------------------------------------------------------

type deliverTool struct{ svc *Service }

func (t *deliverTool) Name() string           { return ToolWorkflowDeliver }
func (t *deliverTool) Class() string          { return "write" }
func (t *deliverTool) ResultBudgetBytes() int { return DefaultDeliverBudgetBytes }
func (t *deliverTool) Description() string {
	return "Perform host-owned delivery for a delivery_pending workflow run. " +
		"Requires explicit allow_publish=true; without it the call refuses publication. " +
		"Only eligible runs publish; other statuses are refused. " +
		"Available to agents by default when the workspace defines workflows; use it to publish a settled run."
}
func (t *deliverTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id waiting for delivery (form wfr-...)",
			},
			"allow_publish": map[string]any{
				"type":        "boolean",
				"description": "Must be true to publish; defaults to false and is never implicit",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}
func (t *deliverTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID        string `json:"run_id"`
		AllowPublish bool   `json:"allow_publish"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowDeliver, err)
	}
	result, err := t.svc.Deliver(ctx, strings.TrimSpace(in.RunID), in.AllowPublish)
	if err != nil {
		return "", err
	}
	return encodeJSON(result, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_cancel
// ---------------------------------------------------------------------------

type cancelTool struct{ svc *Service }

func (t *cancelTool) Name() string           { return ToolWorkflowCancel }
func (t *cancelTool) Class() string          { return "write" }
func (t *cancelTool) ResultBudgetBytes() int { return DefaultCancelBudgetBytes }
func (t *cancelTool) Description() string {
	return "Cancel a running or waiting workflow run. Idempotent: canceling an already-terminal " +
		"run is a no-op. delivery_pending runs must be delivered or cleaned up first. " +
		"Available to agents by default when the workspace defines workflows; use it to stop a run."
}
func (t *cancelTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id to cancel (form wfr-...)",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}
func (t *cancelTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowCancel, err)
	}
	result, err := t.svc.Cancel(ctx, strings.TrimSpace(in.RunID))
	if err != nil {
		return "", err
	}
	return encodeJSON(result, t.ResultBudgetBytes())
}

// ---------------------------------------------------------------------------
// workflow_delete
// ---------------------------------------------------------------------------

type deleteTool struct{ svc *Service }

func (t *deleteTool) Name() string           { return ToolWorkflowDelete }
func (t *deleteTool) Class() string          { return "write" }
func (t *deleteTool) ResultBudgetBytes() int { return DefaultDeleteBudgetBytes }
func (t *deleteTool) Description() string {
	return "Delete a settled workflow run's durable ledger record. Only runs that " +
		"already finished (succeeded, failed, canceled, timed_out, delivery_failed) or " +
		"are waiting for delivery (delivery_pending) can be deleted; active runs must " +
		"be canceled first. The run disappears from every read surface; its worktree " +
		"and branch, if any, are not removed (use the workspace cleanup command for " +
		"those). Shared stored content is never deleted. Available to agents by default " +
		"when the workspace defines workflows; use it to purge stale runs."
}
func (t *deleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run id to delete (form wfr-...)",
			},
		},
		"required":             []string{"run_id"},
		"additionalProperties": false,
	}
}
func (t *deleteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", ToolWorkflowDelete, err)
	}
	result, err := t.svc.Delete(ctx, strings.TrimSpace(in.RunID))
	if err != nil {
		return "", err
	}
	return encodeJSON(result, t.ResultBudgetBytes())
}
