// Package ledger defines immutable identity types, snapshots, and the repository
// boundary for subagent orchestration. It provides the storage abstraction that
// the coordinator depends on, with an in-memory default implementation.
package ledger

import (
	"encoding/json"
	"time"
)

// RunID is a system-generated immutable identifier for one orchestration run.
type RunID string

// TaskID is a system-generated immutable identifier for one DAG node within a run.
type TaskID string

// AttemptID is a system-generated immutable identifier for one execution attempt.
type AttemptID string

// RunStatus represents the lifecycle state of a run.
type RunStatus string

const (
	RunStatusCreated   RunStatus = "created"
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCanceled  RunStatus = "canceled"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusQueued          TaskStatus = "queued"
	TaskStatusRunning         TaskStatus = "running"
	TaskStatusCompleted       TaskStatus = "completed"
	TaskStatusFailed          TaskStatus = "failed"
	TaskStatusTimedOut        TaskStatus = "timed_out"
	TaskStatusCanceled        TaskStatus = "canceled"
	TaskStatusBlocked         TaskStatus = "blocked"
	TaskStatusCancelRequested TaskStatus = "cancel_requested"
	TaskStatusRetryPending    TaskStatus = "retry_pending"
	// TaskStatusAwaitingInput is non-terminal: the task is parked on a
	// question (plan 53.02). Distinct from terminal TaskStatusBlocked
	// (dependency failure; INV-AG-21). May return to running.
	TaskStatusAwaitingInput TaskStatus = "awaiting_input"
)

// RunSnapshot is a defensive-copy snapshot of a single orchestration run.
type RunSnapshot struct {
	RunID              string
	DisplayName        string
	Status             RunStatus // created, queued, running, completed, failed, canceled
	RequestFingerprint string    // canonical coordinator request identity, when provided
	Tasks              []TaskSnapshot
	CreatedAt          time.Time
	CompletedAt        *time.Time
	Labels             map[string]string // caller-provided optional aliases only
	// IdempotencyKey is the caller-supplied deduplication key the run was
	// created under. It is persisted with the run_created payload so a fresh
	// repository replaying the store re-registers the key and refuses a
	// second CreateRun with it, instead of executing the same work twice.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Clone returns a deep copy of the snapshot.
func (s RunSnapshot) Clone() RunSnapshot {
	out := RunSnapshot{
		RunID:              s.RunID,
		DisplayName:        s.DisplayName,
		Status:             s.Status,
		RequestFingerprint: s.RequestFingerprint,
		CreatedAt:          s.CreatedAt,
		CompletedAt:        nil,
		IdempotencyKey:     s.IdempotencyKey,
	}
	if s.Labels != nil {
		out.Labels = make(map[string]string, len(s.Labels))
		for k, v := range s.Labels {
			out.Labels[k] = v
		}
	}
	if s.Tasks != nil {
		out.Tasks = make([]TaskSnapshot, len(s.Tasks))
		for i, t := range s.Tasks {
			out.Tasks[i] = t.Clone()
		}
	}
	if s.CompletedAt != nil {
		t := *s.CompletedAt
		out.CompletedAt = &t
	}
	return out
}

// TaskSnapshot is a defensive-copy snapshot of a single DAG node within a run.
type TaskSnapshot struct {
	RunID        string
	TaskID       string
	ParentTaskID string // empty for root tasks
	DisplayName  string
	Status       string // queued, running, completed, failed, timed_out, canceled, blocked, cancel_requested
	Attempts     []AttemptSnapshot
	DependsOn    []string // TaskIDs this task depends on
	CreatedAt    time.Time
	CompletedAt  *time.Time
	OutputRef    string // bounded redacted reference; empty until completion
	ErrorRef     string // bounded redacted reference; empty unless failed
	Version      uint64 // per-task monotonic version for compare-and-set
	// HandlerName is the registered handler name for the sub-agent task.
	// Stored so ResumeInterruptedRun can rebuild the task config.
	HandlerName string `json:"handler_name,omitempty"`
	// Agent routing metadata describes work, never a durable authority grant.
	// Resume must resolve this name against its current authorized registry.
	AgentName    string `json:"agent_name,omitempty"`
	AgentDigest  string `json:"agent_digest,omitempty"`
	Skill        string `json:"skill,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	Model        string `json:"model,omitempty"`
	// Input is the task payload, stored so a resumed task re-executes the work
	// it was given rather than an empty request.
	//
	// Only fields describing the WORK live here. Permission, scope, role,
	// session and turn are deliberately absent: the ledger is a file in the
	// workspace and the agent has file tools, so a persisted permission would be
	// a privilege grant the agent could write for itself.
	//
	// ParentTaskID above is the one identity-shaped field that IS persisted - it
	// is derived from Task.Owner (coordinator/spawn.go, via parentTaskID) and
	// records DAG parentage, which resume needs. It is deliberately NOT restored
	// into Task.Owner: doing so would make a resumed run'"'"'s dispatcher ParentID and
	// provenance attributable to a workspace-writable file.
	// TestResumeDoesNotRestoreAuthorityFields is the tripwire. See plan 12 §3.
	Input json.RawMessage `json:"input,omitempty"`
	// Timeout, Budget and Depth are resource limits: restored on resume, but
	// clamped to the live configuration so the ledger cannot raise a ceiling.
	Timeout time.Duration `json:"timeout,omitempty"`
	Budget  int           `json:"budget,omitempty"`
	Depth   int           `json:"depth,omitempty"`
}

// Clone returns a deep copy of the snapshot.
func (s TaskSnapshot) Clone() TaskSnapshot {
	out := TaskSnapshot{
		RunID:        s.RunID,
		TaskID:       s.TaskID,
		ParentTaskID: s.ParentTaskID,
		DisplayName:  s.DisplayName,
		Status:       s.Status,
		CreatedAt:    s.CreatedAt,
		CompletedAt:  nil,
		OutputRef:    s.OutputRef,
		ErrorRef:     s.ErrorRef,
		Version:      s.Version,
		HandlerName:  s.HandlerName,
		AgentName:    s.AgentName,
		AgentDigest:  s.AgentDigest,
		Skill:        s.Skill,
		ProviderName: s.ProviderName,
		Model:        s.Model,
		Timeout:      s.Timeout,
		Budget:       s.Budget,
		Depth:        s.Depth,
	}
	if s.Input != nil {
		out.Input = append(json.RawMessage(nil), s.Input...)
	}
	if s.Attempts != nil {
		out.Attempts = make([]AttemptSnapshot, len(s.Attempts))
		for i, a := range s.Attempts {
			out.Attempts[i] = a.Clone()
		}
	}
	if s.DependsOn != nil {
		out.DependsOn = make([]string, len(s.DependsOn))
		copy(out.DependsOn, s.DependsOn)
	}
	if s.CompletedAt != nil {
		t := *s.CompletedAt
		out.CompletedAt = &t
	}
	return out
}

// AttemptSnapshot captures one execution attempt for a task.
type AttemptSnapshot struct {
	AttemptID  string
	TaskID     string
	RunID      string
	AttemptNum int
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
}

// Clone returns a deep copy of the snapshot.
func (s AttemptSnapshot) Clone() AttemptSnapshot {
	out := AttemptSnapshot{
		AttemptID:  s.AttemptID,
		TaskID:     s.TaskID,
		RunID:      s.RunID,
		AttemptNum: s.AttemptNum,
		StartedAt:  s.StartedAt,
		FinishedAt: nil,
		Status:     s.Status,
	}
	if s.FinishedAt != nil {
		t := *s.FinishedAt
		out.FinishedAt = &t
	}
	return out
}

// LifecycleEvent is an append-only event for a run.
type LifecycleEvent struct {
	ID        string // unique event identifier (idempotency key)
	RunID     string
	Sequence  uint64 // monotonic per-run
	Kind      string // e.g. "task_created", "task_completed", "run_canceled"
	TaskID    string // empty for run-level events
	AttemptID string // empty for task-level or run-level events
	Payload   []byte // bounded, redacted; nil for most events
	CreatedAt time.Time
}

// Clone returns a deep copy of the event.
func (e LifecycleEvent) Clone() LifecycleEvent {
	out := LifecycleEvent{
		ID:        e.ID,
		RunID:     e.RunID,
		Sequence:  e.Sequence,
		Kind:      e.Kind,
		TaskID:    e.TaskID,
		AttemptID: e.AttemptID,
		CreatedAt: e.CreatedAt,
		Payload:   nil,
	}
	if e.Payload != nil {
		out.Payload = make([]byte, len(e.Payload))
		copy(out.Payload, e.Payload)
	}
	return out
}
