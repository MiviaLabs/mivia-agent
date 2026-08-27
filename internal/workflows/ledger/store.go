package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Store is the durable, concurrency-safe plan and task ledger. It is
// event-sourced over a shared storage.Store (the same primitive the workflow
// ledger builds on): every mutation appends one durable event, and the
// in-memory projection is rebuilt from the event log on catch-up, so state
// survives restarts and is atomic per mutation. The package is a NON-OWNING
// user of the store: it never closes it.
type Store struct {
	store  storage.Store
	engine *ledgercore.Engine

	mu sync.Mutex
	// plans holds the derived projection, keyed by plan ref.
	plans map[string]*planState
	// now is the swappable clock (SetTimeSource) for journal timestamps.
	now func() time.Time
}

// planState is the derived projection for one plan: the plan record, its
// tasks, and its append-only transition journal in call order.
type planState struct {
	plan    Plan
	tasks   map[string]Task
	journal []Transition
	// binds counts tks_plan_bound events so each re-bind mints a distinct
	// deterministic event ID (derived state, rebuilt from the event log).
	binds int
}

// NewStore wraps a shared storage.Store (non-owning).
func NewStore(store storage.Store) *Store {
	return &Store{
		store:  store,
		engine: ledgercore.NewEngine(store, false, ""),
		plans:  make(map[string]*planState),
		now:    time.Now,
	}
}

// NewMemoryStore returns a store over a fresh in-memory backend.
func NewMemoryStore() *Store { return NewStore(storage.NewMemory()) }

// SetTimeSource replaces the clock for deterministic tests.
func (s *Store) SetTimeSource(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// StorePlan durably stores a plan and returns its ref (the plan ID).
// Re-storing an identical record is an idempotent no-op (recovery re-entry);
// the same ref with different content returns ErrTaskDuplicate.
func (s *Store) StorePlan(plan Plan) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if plan.ID == "" {
		return "", ErrInvalidPlan
	}
	if !ValidScopeType(plan.Scope.Type) {
		return "", ErrInvalidScope
	}
	if err := s.catchUp(context.Background()); err != nil {
		return "", err
	}
	if existing, ok := s.plans[plan.ID]; ok {
		if reflect.DeepEqual(existing.plan, plan) {
			return plan.ID, nil
		}
		return "", ErrTaskDuplicate
	}
	s.plans[plan.ID] = &planState{plan: plan, tasks: make(map[string]Task)}
	err := s.marshalAndAppend(planRunID(plan.ID), eventID(plan.ID, eventKindPlanStored),
		eventKindPlanStored, planStoredPayload{Plan: plan, CreatedAt: s.now()})
	if err != nil {
		return "", err
	}
	return plan.ID, nil
}

// BindPlanToScope re-binds an existing plan to a scope. Binding the scope it
// already carries is an idempotent no-op.
func (s *Store) BindPlanToScope(planID string, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidScopeType(scope.Type) {
		return ErrInvalidScope
	}
	if err := s.catchUp(context.Background()); err != nil {
		return err
	}
	state, ok := s.plans[planID]
	if !ok {
		return ErrPlanNotFound
	}
	if state.plan.Scope == scope {
		return nil
	}
	state.plan.Scope = scope
	state.binds++
	return s.marshalAndAppend(planRunID(planID),
		eventID(planID, eventKindPlanBound, strconv.Itoa(state.binds)),
		eventKindPlanBound, planBoundPayload{Scope: scope, CreatedAt: s.now()})
}

// CreateTask durably records a task under an existing plan. Re-creating an
// identical record is an idempotent no-op; the same (plan, task) with
// different content returns ErrTaskDuplicate.
func (s *Store) CreateTask(task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task.ID == "" || task.PlanRef == "" {
		return ErrInvalidTask
	}
	if !ValidScopeType(task.Scope.Type) {
		return ErrInvalidScope
	}
	if task.Status == "" {
		return ErrEmptyStatus
	}
	if err := s.catchUp(context.Background()); err != nil {
		return err
	}
	state, ok := s.plans[task.PlanRef]
	if !ok {
		return ErrPlanNotFound
	}
	if existing, ok := state.tasks[task.ID]; ok {
		if reflect.DeepEqual(existing, task) {
			return nil
		}
		return ErrTaskDuplicate
	}
	state.tasks[task.ID] = task
	return s.marshalAndAppend(planRunID(task.PlanRef), eventID(task.PlanRef, eventKindTaskCreated, task.ID),
		eventKindTaskCreated, taskCreatedPayload{Task: task, CreatedAt: s.now()})
}

// TransitionTask atomically changes a task status: ONE durable event carries
// the change and its journal timestamp, so the transition and the append-only
// journal entry are indivisible and ordered by call sequence. The status is
// opaque; only non-empty is validated.
func (s *Store) TransitionTask(planRef, taskID, newStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.transitionTaskLocked(planRef, taskID, nil, newStatus)
	return err
}

// TransitionTaskCAS atomically changes a task status only when its current
// status is one of fromStatuses (compare-and-swap). ok=false with a nil
// error means the precondition did not hold: some other caller already moved
// the task, and this caller cleanly lost the race rather than double-admitting
// it. Store.mu already serializes every call (see mu's doc comment), so the
// check and the write are indivisible with respect to any other Store method.
func (s *Store) TransitionTaskCAS(planRef, taskID string, fromStatuses []string, newStatus string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionTaskLocked(planRef, taskID, fromStatuses, newStatus)
}

// TransitionTaskCASDecide atomically reads the task's current status and its
// reopened-attempt count (the count of prior transitions into reopenStatus),
// then applies decide's verdict, all inside one critical section. This closes
// the read-then-decide-then-write race a separate attempt-count read plus a
// later TransitionTaskCAS call would still have: two concurrent failure
// handlers for the same task cannot both observe the same attempt count and
// both decide to reopen, because the second handler's decide call runs after
// the first's write has already landed and sees the incremented count.
//
// fromStatuses gates eligibility exactly like TransitionTaskCAS: decide is
// only invoked when the task's current status is one of fromStatuses;
// otherwise this returns (false, "", 0, nil), the same clean-loss shape.
func (s *Store) TransitionTaskCASDecide(planRef, taskID string, fromStatuses []string, reopenStatus string, decide func(attempts int) (newStatus string, apply bool)) (applied bool, newStatus string, attempts int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.catchUp(context.Background()); err != nil {
		return false, "", 0, err
	}
	state, ok := s.plans[planRef]
	if !ok {
		return false, "", 0, ErrPlanNotFound
	}
	task, ok := state.tasks[taskID]
	if !ok {
		return false, "", 0, ErrTaskNotFound
	}
	if fromStatuses != nil {
		matched := false
		for _, want := range fromStatuses {
			if task.Status == want {
				matched = true
				break
			}
		}
		if !matched {
			return false, "", 0, nil
		}
	}
	for _, tr := range state.journal {
		if tr.TaskID == taskID && tr.ToStatus == reopenStatus {
			attempts++
		}
	}
	want, apply := decide(attempts)
	if !apply {
		return false, "", attempts, nil
	}
	ok2, err := s.transitionTaskLocked(planRef, taskID, fromStatuses, want)
	return ok2, want, attempts, err
}

// transitionTaskLocked is the shared implementation for TransitionTask and
// TransitionTaskCAS. Callers must hold s.mu. A nil fromStatuses applies
// unconditionally (TransitionTask's semantics); a non-nil fromStatuses only
// applies when the task's current status appears in it, returning
// (false, nil) otherwise.
func (s *Store) transitionTaskLocked(planRef, taskID string, fromStatuses []string, newStatus string) (bool, error) {
	if newStatus == "" {
		return false, ErrEmptyStatus
	}
	if err := s.catchUp(context.Background()); err != nil {
		return false, err
	}
	state, ok := s.plans[planRef]
	if !ok {
		return false, ErrPlanNotFound
	}
	task, ok := state.tasks[taskID]
	if !ok {
		return false, ErrTaskNotFound
	}
	if fromStatuses != nil {
		matched := false
		for _, want := range fromStatuses {
			if task.Status == want {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	// The ordinal is derived from durable state (transitions already in the
	// journal), so two writers of the same task agree on the event ID; the
	// loser retries after catch-up (see appendEvent).
	ordinal := 0
	for _, tr := range state.journal {
		if tr.TaskID == taskID {
			ordinal++
		}
	}
	ordinal++
	from := task.Status
	at := s.now()
	task.Status = newStatus
	state.tasks[taskID] = task
	state.journal = append(state.journal, Transition{
		PlanRef: planRef, TaskID: taskID,
		FromStatus: from, ToStatus: newStatus, At: at,
	})
	if err := s.marshalAndAppend(planRunID(planRef), eventID(planRef, eventKindTaskTransitioned, taskID, strconv.Itoa(ordinal)),
		eventKindTaskTransitioned, taskTransitionedPayload{TaskID: taskID, FromStatus: from, ToStatus: newStatus, CreatedAt: at}); err != nil {
		return false, err
	}
	return true, nil
}

// ListTasksByScope returns defensive copies of every task bound to scope.
// A scope with an empty ID matches every ID of that type (for example
// Scope{Type: ScopeRun, ID: ""} returns all run-bound tasks). Order is
// deterministic (plan ref, then task ID).
func (s *Store) ListTasksByScope(scope Scope) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidScopeType(scope.Type) {
		return nil, ErrInvalidScope
	}
	if err := s.catchUp(context.Background()); err != nil {
		return nil, err
	}
	var out []Task
	for _, ref := range sortedPlanRefs(s.plans) {
		state := s.plans[ref]
		for _, id := range sortedTaskIDs(state.tasks) {
			if t := state.tasks[id]; scopeMatches(t.Scope, scope) {
				out = append(out, t.Clone())
			}
		}
	}
	return out, nil
}

// scopeMatches reports whether a task's scope answers a query: the types must
// match, and an empty query ID is a wildcard over IDs of that type.
func scopeMatches(have, want Scope) bool {
	return have.Type == want.Type && (want.ID == "" || have.ID == want.ID)
}

// GetTask returns a defensive copy of one task.
func (s *Store) GetTask(planRef, taskID string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.catchUp(context.Background()); err != nil {
		return Task{}, err
	}
	state, ok := s.plans[planRef]
	if !ok {
		return Task{}, ErrPlanNotFound
	}
	t, ok := state.tasks[taskID]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	return t.Clone(), nil
}

// ReadBackPlan returns a defensive copy of a stored plan by ref.
func (s *Store) ReadBackPlan(planRef string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.catchUp(context.Background()); err != nil {
		return Plan{}, err
	}
	state, ok := s.plans[planRef]
	if !ok {
		return Plan{}, ErrPlanNotFound
	}
	return state.plan.Clone(), nil
}

// ListTransitions returns the plan's append-only journal in call order.
func (s *Store) ListTransitions(planRef string) ([]Transition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.catchUp(context.Background()); err != nil {
		return nil, err
	}
	state, ok := s.plans[planRef]
	if !ok {
		return nil, ErrPlanNotFound
	}
	out := make([]Transition, len(state.journal))
	for i, tr := range state.journal {
		out[i] = tr.Clone()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Append pipeline: one sequence-unique event per mutation
// ---------------------------------------------------------------------------

// marshalAndAppend marshals the payload, mints the event, and appends it via
// AppendBatch: the batch path enforces per-run sequence uniqueness on BOTH
// backends (the memory store checks it only in appendBatchLocked; SQLite has
// a (run_id, sequence) UNIQUE index), so a concurrent writer that takes the
// same sequence fails with ErrTaskDuplicate instead of silently sharing it.
// Called with s.mu held; the projection was already mutated by the caller, so
// any append failure rebuilds the run from the store before returning.
func (s *Store) marshalAndAppend(runID, id, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		_ = s.rebuildRunFromStore(context.Background(), runID)
		return fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	evt := storage.Event{
		ID:       id,
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     kind,
		Payload:  data,
	}
	return s.appendEvent(context.Background(), []storage.Event{evt})
}

// appendEvent writes the events to the store via AppendBatch. On success it
// advances the applied watermark past the events' sequences so catch-up never
// re-reads this instance's own writes.
//
// On ErrTaskDuplicate (the store's id PRIMARY KEY or the (run_id, sequence)
// UNIQUE constraint) it rebuilds the run from the store, then compares the
// stored event with the SAME ID: a byte-identical payload means an idempotent
// retry (nil); a different payload means the logical key was taken by a
// concurrent writer (ErrTaskConflict).
//
// On any other append error it rebuilds the run so the in-memory state
// matches durable state before returning.
func (s *Store) appendEvent(ctx context.Context, events []storage.Event) error {
	batch, ok := s.store.(storage.BatchAppender)
	if !ok {
		return fmt.Errorf("store %T does not implement storage.BatchAppender: "+
			"atomic batch append is required for sequence-unique writes", s.store)
	}
	err := batch.AppendBatch(ctx, events)
	if err == nil {
		for _, evt := range events {
			s.engine.Watermarks().SetApplied(evt.RunID, uint64(evt.Sequence))
		}
		return nil
	}
	evt := events[0]
	if rerr := s.rebuildRunFromStore(ctx, evt.RunID); rerr != nil {
		return fmt.Errorf("store append: %v; rebuild: %w", err, rerr)
	}
	if !errors.Is(err, storage.ErrDuplicate) {
		return fmt.Errorf("store append: %w", err)
	}
	events, rerr := s.store.Events(ctx, evt.RunID)
	if rerr != nil {
		return fmt.Errorf("read events after duplicate: %w", rerr)
	}
	for _, e := range events {
		if e.ID == evt.ID {
			if bytes.Equal(e.Payload, evt.Payload) {
				return nil // idempotent retry
			}
			return ErrTaskConflict
		}
	}
	// The duplicate came from the (run_id, sequence) UNIQUE constraint with
	// no event carrying our ID: the sequence was lost to another writer.
	return ErrTaskConflict
}
