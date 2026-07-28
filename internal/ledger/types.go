// Package ledger defines immutable identity types, snapshots, and the repository
// boundary for subagent orchestration. It provides the storage abstraction that
// the coordinator depends on, with an in-memory default implementation.
package ledger

import "time"

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
)

// RunSnapshot is a defensive-copy snapshot of a single orchestration run.
type RunSnapshot struct {
	RunID       string
	DisplayName string
	Status      RunStatus // created, queued, running, completed, failed, canceled
	Tasks       []TaskSnapshot
	CreatedAt   time.Time
	CompletedAt *time.Time
	Labels      map[string]string // caller-provided optional aliases only
}

// Clone returns a deep copy of the snapshot.
func (s RunSnapshot) Clone() RunSnapshot {
	out := RunSnapshot{
		RunID:       s.RunID,
		DisplayName: s.DisplayName,
		Status:      s.Status,
		CreatedAt:   s.CreatedAt,
		CompletedAt: nil,
		Labels:      make(map[string]string, len(s.Labels)),
		Tasks:       make([]TaskSnapshot, len(s.Tasks)),
	}
	if s.CompletedAt != nil {
		t := *s.CompletedAt
		out.CompletedAt = &t
	}
	for k, v := range s.Labels {
		out.Labels[k] = v
	}
	for i, t := range s.Tasks {
		out.Tasks[i] = t.Clone()
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
}

// Clone returns a deep copy of the snapshot.
func (s TaskSnapshot) Clone() TaskSnapshot {
	out := TaskSnapshot{
		RunID:        s.RunID,
		TaskID:       s.TaskID,
		ParentTaskID: s.ParentTaskID,
		DisplayName:  s.DisplayName,
		Status:       s.Status,
		Attempts:     make([]AttemptSnapshot, len(s.Attempts)),
		DependsOn:    make([]string, len(s.DependsOn)),
		CreatedAt:    s.CreatedAt,
		CompletedAt:  nil,
		OutputRef:    s.OutputRef,
		ErrorRef:     s.ErrorRef,
		Version:      s.Version,
	}
	copy(out.DependsOn, s.DependsOn)
	for i, a := range s.Attempts {
		out.Attempts[i] = a.Clone()
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
