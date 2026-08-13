// Package tasks provides a generic, durable plan and task ledger (D8).
//
// One mechanism serves many scopes: sessions, workflow steps, agents,
// workflows and runs all store plans and task statuses through the same API.
// The engine stores, transitions and queries; consumers define their own
// status vocabulary. Statuses are OPAQUE strings: this package never
// interprets them, only validates non-empty and journals transitions.
//
// Durability: every mutation appends one event to a shared storage.Store
// (the same primitive the workflow ledger builds on). The in-memory
// projection is rebuilt from the event log on catch-up, so state survives
// restarts and each mutation is atomic with its journal entry.
package tasks

import (
	"errors"
	"time"
)

// Scope types bind a plan or a task to one engine entity. The type and ID
// are opaque; consumers choose their own vocabulary.
const (
	ScopeSession  = "session"
	ScopeStep     = "step"
	ScopeAgent    = "agent"
	ScopeWorkflow = "workflow"
	ScopeRun      = "run"
)

// Sentinel errors returned by Store methods.
var (
	// ErrPlanNotFound reports a plan ref that has no stored plan.
	ErrPlanNotFound = errors.New("plan not found")
	// ErrTaskNotFound reports a task that has no record under its plan.
	ErrTaskNotFound = errors.New("task not found")
	// ErrInvalidScope reports a scope type outside the supported set.
	ErrInvalidScope = errors.New("invalid scope type")
	// ErrEmptyStatus reports an empty status where a non-empty one is required.
	ErrEmptyStatus = errors.New("status must not be empty")
	// ErrInvalidPlan reports a plan record that cannot be stored (empty ref).
	ErrInvalidPlan = errors.New("invalid plan")
	// ErrInvalidTask reports a task record that cannot be created (empty id or plan ref).
	ErrInvalidTask = errors.New("invalid task")
	// ErrDuplicate reports a record that already exists with different content.
	ErrDuplicate = errors.New("duplicate record")
	// ErrConflict reports a logical key taken by a concurrent writer.
	ErrConflict = errors.New("state conflict")
)

// Scope identifies one engine entity: a session, a workflow step, an agent,
// a workflow, or a run.
type Scope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ValidScopeType reports whether t is one of the supported scope types.
func ValidScopeType(t string) bool {
	switch t {
	case ScopeSession, ScopeStep, ScopeAgent, ScopeWorkflow, ScopeRun:
		return true
	default:
		return false
	}
}

// Plan is a durable engine-ledger artifact. The ref returned by StorePlan is
// the plan ID; ReadBackPlan(planRef) reads the record back by that ref. The
// payload itself lives in content-addressed storage under PayloadRef; this
// package treats the ref as opaque metadata.
type Plan struct {
	ID         string    `json:"id"`
	Scope      Scope     `json:"scope"`
	Schema     string    `json:"schema,omitempty"`
	PayloadRef string    `json:"payload_ref,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
}

// Clone returns a defensive copy.
func (p Plan) Clone() Plan { return p }

// Task is a durable work item under one plan. Status is an opaque string;
// only non-empty is validated. Transitions are appended to the plan's
// journal (see ListTransitions).
type Task struct {
	ID        string   `json:"id"`
	PlanRef   string   `json:"plan_ref"`
	Scope     Scope    `json:"scope"`
	Status    string   `json:"status"`
	RunRef    string   `json:"run_ref,omitempty"`
	PRNumber  string   `json:"pr_number,omitempty"`
	Deps      []string `json:"deps,omitempty"`
	Attempts  int      `json:"attempts,omitempty"`
	LastError string   `json:"last_error,omitempty"`
}

// Clone returns a defensive copy.
func (t Task) Clone() Task {
	t.Deps = append([]string(nil), t.Deps...)
	return t
}

// Transition is one durable journal entry: an atomic status change with its
// timestamp. The journal is append-only; order matches call order.
type Transition struct {
	PlanRef    string    `json:"plan_ref"`
	TaskID     string    `json:"task_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	At         time.Time `json:"at"`
}

// Clone returns a defensive copy.
func (t Transition) Clone() Transition { return t }
