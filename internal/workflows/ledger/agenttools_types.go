// Package agenttools exposes in-process workflow tools for the agent surface.
// Tools call the shared workflow ledger for reads and an injected Engine for
// mutations. This package must not import controller/agents/skills so the
// tools package can import it without an import cycle.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// InvocationRunID returns the stable workflow run ID for a caller key.
func InvocationRunID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "wfr-inv-" + hex.EncodeToString(sum[:16])
}

// Tool names are model-facing and project/language-generic (rule 60).
const (
	ToolWorkflowRun      = "workflow_run"
	ToolWorkflowStatus   = "workflow_status"
	ToolWorkflowEvents   = "workflow_events"
	ToolWorkflowInspect  = "workflow_inspect"
	ToolWorkflowListRuns = "workflow_list_runs"
	ToolWorkflowDeliver  = "workflow_deliver"
	ToolWorkflowCancel   = "workflow_cancel"
	ToolWorkflowDelete   = "workflow_delete"
)

// AllToolNames returns the eight Phase 7 workflow tool names in stable order.
func AllToolNames() []string {
	return []string{
		ToolWorkflowRun,
		ToolWorkflowStatus,
		ToolWorkflowEvents,
		ToolWorkflowInspect,
		ToolWorkflowListRuns,
		ToolWorkflowDeliver,
		ToolWorkflowCancel,
		ToolWorkflowDelete,
	}
}

// Result budgets bound tool JSON (INV-AG-25). Framing stays inside the budget.
const (
	DefaultStatusBudgetBytes  = 256 << 10
	DefaultEventsBudgetBytes  = 256 << 10
	DefaultInspectBudgetBytes = 512 << 10
	DefaultListBudgetBytes    = 128 << 10
	DefaultRunBudgetBytes     = 16 << 10
	DefaultDeliverBudgetBytes = 32 << 10
	DefaultCancelBudgetBytes  = 16 << 10
	DefaultDeleteBudgetBytes  = 16 << 10
	DefaultEventsPageSize     = 50
	DefaultListRunsPageSize   = 50

	// DefaultInspectPageBytes is the default page size for workflow_inspect
	// output text (INV-AG-25): one page of redacted, rune-safe text.
	DefaultInspectPageBytes = 64 << 10
	// MaxPageableBytes is the total artifact size beyond which
	// workflow_inspect refuses to page output at all (clear refusal).
	MaxPageableBytes = 8 << 20
)

// StartRequest admits a new workflow run or resumes an interrupted one.
type StartRequest struct {
	// Workflow is the discovered workflow name (required for a new run).
	Workflow string
	// Inputs are validated name→value pairs from the tool call.
	Inputs map[string]any
	// InvocationKey identifies one caller request across retries. When set for
	// a new run, the engine derives a stable run ID and admits it once.
	InvocationKey string
	// AllowPublish is the explicit publication gate for the workflow_deliver
	// tool and the CLI --allow-publish flag. The session harness does NOT
	// consult it for auto-delivery: a workflow whose [delivery] policy is
	// active is published automatically (the policy is the publication
	// grant), so a delivery-capable run is never stranded by a missing flag.
	AllowPublish bool
	// Resume, when true, resumes RunID from the durable ledger snapshot.
	Resume bool
	// RunID is required when Resume is true.
	RunID string
	// Force clears a stale claim before resume (operator-confirmed).
	Force bool
}

// StartResult is the immediate response from workflow_run (non-blocking).
type StartResult struct {
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Workflow string `json:"workflow,omitempty"`
	Resumed  bool   `json:"resumed,omitempty"`
}

// StatusView is the Level-1 observability payload for workflow_status.
type StatusView struct {
	RunID      string         `json:"run_id"`
	Workflow   string         `json:"workflow"`
	Status     string         `json:"status"`
	ActiveStep string         `json:"active_step"`
	Version    uint64         `json:"version"`
	StartedAt  string         `json:"started_at,omitempty"`
	DeadlineAt string         `json:"deadline_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
	BaseRef    string         `json:"base_ref,omitempty"`
	BaseCommit string         `json:"base_commit,omitempty"`
	Worktree   string         `json:"worktree,omitempty"`
	Attempts   []AttemptView  `json:"attempts"`
	Loops      []LoopView     `json:"loops,omitempty"`
	Delivery   []DeliveryView `json:"delivery,omitempty"`
	Approvals  []ApprovalView `json:"approvals,omitempty"`
	// DeliveryClaimHeld/DeliveryClaimAt mirror RunListItem's fields of the
	// same name - see that doc comment. Only populated for a
	// DELIVERY_PENDING run.
	DeliveryClaimHeld bool   `json:"delivery_claim_held,omitempty"`
	DeliveryClaimAt   string `json:"delivery_claim_at,omitempty"`
}

// AttemptView summarises one numbered step attempt.
type AttemptView struct {
	Step             string `json:"step"`
	Attempt          int    `json:"attempt"`
	Status           string `json:"status"`
	ToStep           string `json:"to_step,omitempty"`
	OutputDigest     string `json:"output_digest,omitempty"`
	OutputRef        string `json:"output_ref,omitempty"`
	ErrorRef         string `json:"error_ref,omitempty"`
	CoordinatorRunID string `json:"coordinator_run_id,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	Verdict          string `json:"verdict,omitempty"`
	MatchDigest      string `json:"match_digest,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	FinishedAt       string `json:"finished_at,omitempty"`
	ElapsedSeconds   int64  `json:"elapsed_seconds,omitempty"`
	// LastHeartbeatAt is the latest liveness observation for a RUNNING
	// attempt, RFC3339 UTC, or empty when none was recorded.
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"`
	// LastHeartbeatStalenessSeconds is the seconds elapsed since the latest
	// heartbeat, or 0 when none was recorded (or the clock is skewed).
	LastHeartbeatStalenessSeconds int64 `json:"last_heartbeat_staleness_seconds,omitempty"`
}

// LoopView is one named loop counter.
type LoopView struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
}

// DeliveryView is one delivery record summary.
type DeliveryView struct {
	IdempotencyKey string `json:"idempotency_key"`
	Status         string `json:"status"`
	Mode           string `json:"mode,omitempty"`
	URL            string `json:"url,omitempty"`
	CommitSHA      string `json:"commit_sha,omitempty"`
	ErrorRef       string `json:"error_ref,omitempty"`
	// ErrorText carries the resolved failure hint for a failed delivery, so
	// the run status surfaces why delivery is pending without an extra lookup.
	ErrorText string `json:"error_text,omitempty"`
}

// ApprovalView is one human-gate approval summary.
type ApprovalView struct {
	ApprovalID string `json:"approval_id"`
	Step       string `json:"step"`
	Status     string `json:"status"`
	Actor      string `json:"actor,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// EventView is one audit-trail entry for workflow_events.
type EventView struct {
	Seq       int    `json:"seq"`
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
}

// EventsPage is a paged audit trail.
type EventsPage struct {
	RunID  string      `json:"run_id"`
	Events []EventView `json:"events"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
	Count  int         `json:"count"`
}

// InspectView is the Level-2 step attempt detail for workflow_inspect.
// The OutputText/OutputBytes/OutputOffset/OutputNextOffset fields page a
// large artifact: OutputText is one redacted, rune-safe text page
// (DefaultInspectPageBytes), OutputBytes is the total artifact size
// (metadata only), OutputOffset is this page's raw-byte offset, and
// OutputNextOffset is the next page's offset (0 when exhausted). Artifacts
// larger than MaxPageableBytes are refused outright.
type InspectView struct {
	RunID             string          `json:"run_id"`
	Step              string          `json:"step"`
	Attempt           int             `json:"attempt"`
	Status            string          `json:"status"`
	CoordinatorRunID  string          `json:"coordinator_run_id,omitempty"`
	TaskID            string          `json:"task_id,omitempty"`
	Output            any             `json:"output,omitempty"`
	OutputRef         string          `json:"output_ref,omitempty"`
	OutputDigest      string          `json:"output_digest,omitempty"`
	OutputText        string          `json:"output_text,omitempty"`
	OutputBytes       int             `json:"output_bytes,omitempty"`
	OutputOffset      int             `json:"output_offset,omitempty"`
	OutputNextOffset  int             `json:"output_next_offset,omitempty"`
	ErrorRef          string          `json:"error_ref,omitempty"`
	ErrorText         string          `json:"error_text,omitempty"`
	EvidenceSelection any             `json:"evidence_selection,omitempty"`
	Transition        *TransitionView `json:"transition,omitempty"`
	StartedAt         string          `json:"started_at,omitempty"`
	FinishedAt        string          `json:"finished_at,omitempty"`
	ElapsedSeconds    int64           `json:"elapsed_seconds,omitempty"`
}

// TransitionView is the durable route decision for one attempt.
type TransitionView struct {
	Index       int            `json:"index"`
	ToStep      string         `json:"to_step,omitempty"`
	MatchDigest string         `json:"match_digest,omitempty"`
	Selected    map[string]any `json:"selected,omitempty"`
}

// ListRunsView lists active and historical runs.
type ListRunsView struct {
	Runs   []RunListItem `json:"runs"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
	Count  int           `json:"count"`
}

// RunListItem is one row from workflow_list_runs.
type RunListItem struct {
	RunID     string `json:"run_id"`
	Workflow  string `json:"workflow"`
	Status    string `json:"status"`
	Age       string `json:"age,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	// ActiveStep is the run's current step id, empty for a terminal run or
	// one that hasn't started its first step yet.
	ActiveStep string `json:"active_step,omitempty"`
	// LastHeartbeatAt mirrors AttemptView's field of the same name, for the
	// active step's newest attempt - RFC3339 UTC, empty when the run is
	// terminal, has no active step, or that attempt hasn't heartbeated yet.
	// A caller rendering a live list (e.g. a desktop app's run list) needs
	// this without a second per-run round trip through workflow_status.
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"`
	// DeliveryClaimHeld and DeliveryClaimAt mirror the TUI's own delivery
	// liveness surface (internal/cli/workflow_run_dialog.go's
	// workflowRunDeliveryClaim / workflowDeliveryClaimLine): only populated
	// for a DELIVERY_PENDING run. Held=true with a fresh ClaimAt means a
	// delivery attempt is actually in flight right now; Held=true with a
	// stale ClaimAt means one crashed mid-publish; Held=false means the run
	// is simply parked waiting for someone to call workflow_deliver. Without
	// this a caller has no way to distinguish "actively delivering" from
	// "waiting indefinitely" for a DELIVERY_PENDING run - LastHeartbeatAt
	// freezes once the run's last step attempt finishes and says nothing
	// about delivery activity.
	DeliveryClaimHeld bool   `json:"delivery_claim_held,omitempty"`
	DeliveryClaimAt   string `json:"delivery_claim_at,omitempty"`
}

// DeliverResult is the response from workflow_deliver.
type DeliverResult struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CancelResult is the response from workflow_cancel.
type CancelResult struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// DeleteResult is the response from workflow_delete. Status is the run's
// status BEFORE deletion; Deleted is always true on success (an error is
// returned otherwise), so the tool output is self-documenting for the agent.
type DeleteResult struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}

// Engine performs mutating workflow operations. Reads use Repository only.
type Engine interface {
	// Start admits a run and advances it in a background goroutine.
	// It returns as soon as the run ID is durable (non-blocking).
	Start(ctx context.Context, req StartRequest) (StartResult, error)
	// Cancel settles a non-terminal run to canceled (idempotent).
	Cancel(ctx context.Context, runID string) (CancelResult, error)
	// Deliver publishes a delivery_pending run when allow_publish is true.
	Deliver(ctx context.Context, runID string, allowPublish bool) (DeliverResult, error)
	// Delete removes a run from the durable ledger. Settled runs (terminal or
	// delivery_pending) are always deletable; with force, a non-terminal run
	// (pending/running/waiting_approval) is deletable too — the crash-recovery
	// override for runs stranded by a dead executor. A fresh claim held by a
	// live executor is refused either way; only an expired lease is taken over.
	Delete(ctx context.Context, runID string, force bool) (DeleteResult, error)
}

// RepoFactory opens a workflow ledger repository. The closer releases resources.
type RepoFactory func(ctx context.Context) (Repository, func(), error)

// ErrRepoUnset is returned when tools register before a ledger is wired.
var ErrRepoUnset = errRepoUnset("workflow ledger is not configured for this session")

type errRepoUnset string

func (e errRepoUnset) Error() string { return string(e) }

// UnsetRepoFactory is used when tools register before a ledger is available.
func UnsetRepoFactory(context.Context) (Repository, func(), error) {
	return nil, func() {}, ErrRepoUnset
}

// formatTime returns RFC3339 UTC or empty for zero.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// formatTimePtr returns RFC3339 UTC or empty for nil/zero.
func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
