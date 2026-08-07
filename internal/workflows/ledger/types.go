package ledger

import (
	"time"
)

type RunStatus string

const (
	RunStatusPending         RunStatus = "pending"
	RunStatusRunning         RunStatus = "running"
	RunStatusWaitingApproval RunStatus = "waiting_approval"
	RunStatusDeliveryPending RunStatus = "delivery_pending"
	RunStatusSucceeded       RunStatus = "succeeded"
	RunStatusFailed          RunStatus = "failed"
	RunStatusCanceled        RunStatus = "canceled"
	RunStatusTimedOut        RunStatus = "timed_out"
	RunStatusDeliveryFailed  RunStatus = "delivery_failed"
)

// ValidRunTransition reports whether a run may move from one status to another.
// Edges: pending->running; running->waiting_approval|delivery_pending|succeeded|failed|canceled|timed_out;
// waiting_approval->running|failed|canceled|timed_out; delivery_pending->succeeded|delivery_failed.
// Terminal statuses (succeeded/failed/canceled/timed_out/delivery_failed) have no outgoing edges.
func ValidRunTransition(from, to RunStatus) bool {
	switch from {
	case RunStatusPending:
		return to == RunStatusRunning
	case RunStatusRunning:
		switch to {
		case RunStatusWaitingApproval, RunStatusDeliveryPending, RunStatusSucceeded,
			RunStatusFailed, RunStatusCanceled, RunStatusTimedOut:
			return true
		}
		return false
	case RunStatusWaitingApproval:
		switch to {
		case RunStatusRunning, RunStatusFailed, RunStatusCanceled, RunStatusTimedOut:
			return true
		}
		return false
	case RunStatusDeliveryPending:
		return to == RunStatusSucceeded || to == RunStatusDeliveryFailed
	default:
		return false
	}
}

// IsTerminalRunStatus reports whether the status is terminal (no outgoing transitions).
func IsTerminalRunStatus(s RunStatus) bool {
	switch s {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCanceled, RunStatusTimedOut, RunStatusDeliveryFailed:
		return true
	default:
		return false
	}
}

// IsResumableRunStatus reports whether the status means the run was interrupted
// and can be resumed: pending, running, waiting_approval. delivery_pending is a
// deliberate terminal-like pause, not an interruption.
func IsResumableRunStatus(s RunStatus) bool {
	switch s {
	case RunStatusPending, RunStatusRunning, RunStatusWaitingApproval:
		return true
	default:
		return false
	}
}

type AttemptStatus string

const (
	AttemptStatusPending     AttemptStatus = "pending"
	AttemptStatusRunning     AttemptStatus = "running"
	AttemptStatusSucceeded   AttemptStatus = "succeeded"
	AttemptStatusFailed      AttemptStatus = "failed"
	AttemptStatusTimedOut    AttemptStatus = "timed_out"
	AttemptStatusCanceled    AttemptStatus = "canceled"
	AttemptStatusInterrupted AttemptStatus = "interrupted"
)

// ValidAttemptTransition reports whether an attempt may move from one status to another.
// Edges: pending->running; running->succeeded|failed|timed_out|canceled|interrupted.
// All of succeeded/failed/timed_out/canceled/interrupted are terminal for the attempt record.
func ValidAttemptTransition(from, to AttemptStatus) bool {
	switch from {
	case AttemptStatusPending:
		return to == AttemptStatusRunning
	case AttemptStatusRunning:
		switch to {
		case AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusTimedOut,
			AttemptStatusCanceled, AttemptStatusInterrupted:
			return true
		}
		return false
	default:
		return false
	}
}

// IsTerminalAttemptStatus reports whether the attempt status is terminal.
func IsTerminalAttemptStatus(s AttemptStatus) bool {
	switch s {
	case AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusTimedOut,
		AttemptStatusCanceled, AttemptStatusInterrupted:
		return true
	default:
		return false
	}
}

// IsTerminalStepID reports whether a step ID is one of the reserved terminal
// steps from the workflow contract ("success", "failure"). A route to either
// means the workflow is done even if the run status CAS was not recorded.
func IsTerminalStepID(stepID string) bool {
	return stepID == "success" || stepID == "failure"
}

type RunSnapshot struct {
	RunID            string     `json:"run_id"`
	WorkflowName     string     `json:"workflow_name"`
	WorkflowDigest   string     `json:"workflow_digest"`
	SnapshotDigest   string     `json:"snapshot_digest"`
	InputDigest      string     `json:"input_digest"`
	Status           RunStatus  `json:"status"`
	ActiveStepID     string     `json:"active_step_id"`
	BaseRef          string     `json:"base_ref,omitempty"`
	BaseCommit       string     `json:"base_commit,omitempty"`
	OriginBaseCommit string     `json:"origin_base_commit,omitempty"`
	WorktreeName     string     `json:"worktree_name,omitempty"`
	RemoteURL        string     `json:"remote_url,omitempty"`
	Version          uint64     `json:"version"`
	StartedAt        time.Time  `json:"started_at"`
	DeadlineAt       *time.Time `json:"deadline_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

// Clone returns a deep copy.
func (s RunSnapshot) Clone() RunSnapshot {
	clone := s
	if s.DeadlineAt != nil {
		t := *s.DeadlineAt
		clone.DeadlineAt = &t
	}
	if s.FinishedAt != nil {
		t := *s.FinishedAt
		clone.FinishedAt = &t
	}
	return clone
}

type StepAttempt struct {
	AttemptID        string        `json:"attempt_id"`
	RunID            string        `json:"run_id"`
	StepID           string        `json:"step_id"`
	AttemptNo        int           `json:"attempt_no"`
	Status           AttemptStatus `json:"status"`
	CoordinatorRunID string        `json:"coordinator_run_id,omitempty"`
	TaskID           string        `json:"task_id,omitempty"`
	OutputRef        string        `json:"output_ref,omitempty"`
	OutputDigest     string        `json:"output_digest,omitempty"`
	ErrorRef         string        `json:"error_ref,omitempty"`
	ToStepID         string        `json:"to_step_id,omitempty"`
	TransitionIndex  int           `json:"transition_index,omitempty"`
	MatchDigest      string        `json:"match_digest,omitempty"`
	DecisionJSON     []byte        `json:"decision_json,omitempty"`
	EvidenceJSON     []byte        `json:"evidence_json,omitempty"`
	StartedAt        time.Time     `json:"started_at"`
	FinishedAt       *time.Time    `json:"finished_at,omitempty"`
	Version          uint64        `json:"version"`
}

// Clone returns a deep copy.
func (s StepAttempt) Clone() StepAttempt {
	clone := s
	clone.DecisionJSON = append([]byte(nil), s.DecisionJSON...)
	clone.EvidenceJSON = append([]byte(nil), s.EvidenceJSON...)
	if s.FinishedAt != nil {
		t := *s.FinishedAt
		clone.FinishedAt = &t
	}
	return clone
}

// AttemptOutcome is the terminal result of one attempt, recorded atomically
// with the attempt's status change (ONE event per mutation). The route fields
// (ToStepID, TransitionIndex, MatchDigest, DecisionJSON) carry the transition
// decision computed from snapshotted typed evidence before the completion is
// persisted; they are empty for interrupted/canceled/timed_out completions.
type AttemptOutcome struct {
	Status           AttemptStatus
	CoordinatorRunID string
	TaskID           string
	OutputRef        string
	OutputDigest     string
	ErrorRef         string
	ToStepID         string
	TransitionIndex  int
	MatchDigest      string
	DecisionJSON     []byte
	EvidenceJSON     []byte
}

// MaxEvidenceBytes bounds persisted evidence-selection metadata.
const MaxEvidenceBytes = 16 << 10

type TransitionRecord struct {
	RunID           string    `json:"run_id"`
	FromAttemptID   string    `json:"from_attempt_id"`
	ToStepID        string    `json:"to_step_id"`
	TransitionIndex int       `json:"transition_index"`
	MatchDigest     string    `json:"match_digest"`
	DecisionJSON    []byte    `json:"decision_json"`
	CreatedAt       time.Time `json:"created_at"`
}

// Clone returns a deep copy.
func (t TransitionRecord) Clone() TransitionRecord {
	clone := t
	clone.DecisionJSON = append([]byte(nil), t.DecisionJSON...)
	return clone
}

type LoopCounter struct {
	RunID      string `json:"run_id"`
	LoopName   string `json:"loop_name"`
	Iterations int    `json:"iterations"`
}

// ApprovalRecord records one human-gate request and its resolution.
type ApprovalRecord struct {
	ApprovalID   string     `json:"approval_id"`
	RunID        string     `json:"run_id"`
	StepID       string     `json:"step_id"`
	Status       string     `json:"status"` // pending | approved | rejected
	Actor        string     `json:"actor,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	EvidenceJSON []byte     `json:"evidence_json,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
}

// Clone returns a deep copy.
func (a ApprovalRecord) Clone() ApprovalRecord {
	clone := a
	clone.EvidenceJSON = append([]byte(nil), a.EvidenceJSON...)
	if a.ResolvedAt != nil {
		t := *a.ResolvedAt
		clone.ResolvedAt = &t
	}
	return clone
}

// DeliveryRecord records the retry-safe publish lifecycle for one run.
type DeliveryRecord struct {
	RunID          string    `json:"run_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Mode           string    `json:"mode"`
	BaseRef        string    `json:"base_ref"`
	HeadRef        string    `json:"head_ref,omitempty"`
	CommitSHA      string    `json:"commit_sha,omitempty"`
	TreeSHA        string    `json:"tree_sha,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	RemoteID       string    `json:"remote_id,omitempty"`
	URL            string    `json:"url,omitempty"`
	Status         string    `json:"status"`
	ErrorRef       string    `json:"error_ref,omitempty"`
	DiffRef        string    `json:"diff_ref,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Clone returns a deep copy.
func (d DeliveryRecord) Clone() DeliveryRecord { return d }
