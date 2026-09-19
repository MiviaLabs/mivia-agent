package ledger

import "time"

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

// FormatTime returns RFC3339 UTC or empty for zero.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// FormatTimePtr returns RFC3339 UTC or empty for nil/zero.
func FormatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Inspect paging bounds for workflow_inspect output.
const (
	// DefaultInspectPageBytes is the default page size for workflow_inspect
	// output text (INV-AG-25): one page of redacted, rune-safe text.
	DefaultInspectPageBytes = 64 << 10
	// MaxPageableBytes is the total artifact size beyond which
	// workflow_inspect refuses to page output at all (clear refusal).
	MaxPageableBytes = 8 << 20
)
