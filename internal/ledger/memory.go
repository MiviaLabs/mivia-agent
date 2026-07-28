package ledger

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryLedgerRepository is an in-memory implementation of LedgerRepository.
// It uses sync.RWMutex for concurrency safety and returns defensive copies
// on all read paths. It is the default backend for Phase 1 and suitable for
// unit and race tests.
type MemoryLedgerRepository struct {
	mu     sync.RWMutex
	runs   map[string]*runRecord
	closed bool
	now    func() time.Time // injectable time source for tests
}

type runRecord struct {
	snapshot  RunSnapshot
	closed    bool
	tasks     map[string]*taskRecord
	events    []LifecycleEvent
	sequences map[string]uint64 // runID -> next sequence
	idemKeys  map[string]string // idempotency key -> runID
}

type taskRecord struct {
	snapshot TaskSnapshot
}

// NewMemoryLedgerRepository creates a new empty in-memory ledger repository.
func NewMemoryLedgerRepository() *MemoryLedgerRepository {
	return &MemoryLedgerRepository{
		runs: map[string]*runRecord{},
		now:  time.Now,
	}
}

// SetTimeSource replaces the clock for deterministic tests.
func (m *MemoryLedgerRepository) SetTimeSource(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func (m *MemoryLedgerRepository) CreateRun(_ context.Context, key string, snapshot RunSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, ok := m.runs[snapshot.RunID]; ok {
		return ErrDuplicate
	}
	if key != "" {
		for _, rec := range m.runs {
			if rec.idemKeys != nil {
				if _, ok := rec.idemKeys[key]; ok {
					return ErrDuplicate
				}
			}
		}
	}
	rec := &runRecord{
		snapshot:  snapshot.Clone(),
		tasks:     map[string]*taskRecord{},
		events:    make([]LifecycleEvent, 0, 16),
		sequences: map[string]uint64{},
		idemKeys:  map[string]string{},
	}
	if key != "" {
		rec.idemKeys[key] = snapshot.RunID
	}
	rec.snapshot.CreatedAt = m.now()
	m.runs[snapshot.RunID] = rec
	return nil
}

func (m *MemoryLedgerRepository) GetRun(_ context.Context, runID string) (RunSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.runs[runID]
	if !ok {
		return RunSnapshot{}, ErrNotFound
	}
	return rec.fullSnapshot(m.now), nil
}

func (m *MemoryLedgerRepository) ListRuns(_ context.Context, status ...RunStatus) ([]RunSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []RunSnapshot
	for _, rec := range m.runs {
		snap := rec.fullSnapshot(m.now)
		if len(status) == 0 {
			out = append(out, snap)
		} else {
			for _, s := range status {
				if snap.Status == s {
					out = append(out, snap)
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}

func (m *MemoryLedgerRepository) CreateTask(_ context.Context, snap TaskSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.runs[snap.RunID]
	if !ok {
		return ErrNotFound
	}
	if rec.closed {
		return ErrClosed
	}
	if _, ok := rec.tasks[snap.TaskID]; ok {
		return ErrDuplicate
	}
	rec.tasks[snap.TaskID] = &taskRecord{snapshot: snap.Clone()}
	return nil
}

func (m *MemoryLedgerRepository) GetTask(_ context.Context, runID, taskID string) (TaskSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.runs[runID]
	if !ok {
		return TaskSnapshot{}, ErrNotFound
	}
	trec, ok := rec.tasks[taskID]
	if !ok {
		return TaskSnapshot{}, ErrNotFound
	}
	return trec.snapshot.Clone(), nil
}

func (m *MemoryLedgerRepository) ListTasks(_ context.Context, runID string) ([]TaskSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.runs[runID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]TaskSnapshot, 0, len(rec.tasks))
	for _, trec := range rec.tasks {
		out = append(out, trec.snapshot.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryLedgerRepository) AppendEvent(_ context.Context, event LifecycleEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.runs[event.RunID]
	if !ok {
		return ErrNotFound
	}
	// Check duplicate by event ID
	for _, ev := range rec.events {
		if ev.ID == event.ID {
			return ErrDuplicate
		}
	}
	seq := rec.sequences[event.RunID] + 1
	event.Sequence = seq
	event.CreatedAt = m.now()
	rec.sequences[event.RunID] = seq
	rec.events = append(rec.events, event.Clone())
	return nil
}

func (m *MemoryLedgerRepository) ListEvents(_ context.Context, runID string) ([]LifecycleEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.runs[runID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]LifecycleEvent, len(rec.events))
	for i, ev := range rec.events {
		out[i] = ev.Clone()
	}
	return out, nil
}

func (m *MemoryLedgerRepository) CompareAndSetTaskStatus(_ context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if strings.TrimSpace(newStatus) == "" {
		return fmt.Errorf("empty status")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if rec.closed {
		return ErrClosed
	}
	trec, ok := rec.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	if trec.snapshot.Version != expectedVersion {
		return ErrConflict
	}
	if !ValidTaskTransition(trec.snapshot.Status, newStatus) {
		return ErrInvalidTransition
	}
	trec.snapshot.Status = newStatus
	trec.snapshot.Version++
	now := m.now()
	if isTerminalTaskStatus(newStatus) && trec.snapshot.CompletedAt == nil {
		trec.snapshot.CompletedAt = &now
	}
	// Update run status if all tasks are terminal
	m.updateRunStatusLocked(rec)
	return nil
}

func (m *MemoryLedgerRepository) SetTaskOutput(_ context.Context, runID, taskID string, outputRef, errorRef string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if rec.closed {
		return ErrClosed
	}
	trec, ok := rec.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	trec.snapshot.OutputRef = outputRef
	trec.snapshot.ErrorRef = errorRef
	return nil
}

func (m *MemoryLedgerRepository) CloseRun(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if rec.closed {
		return ErrInvalidTransition
	}
	rec.closed = true
	return nil
}

func (m *MemoryLedgerRepository) DeleteRun(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return ErrNotFound
	}
	delete(m.runs, runID)
	return nil
}

// fullSnapshot reconstructs a complete RunSnapshot from the record.
func (r *runRecord) fullSnapshot(now func() time.Time) RunSnapshot {
	snap := r.snapshot.Clone()
	// Rebuild tasks slice from task map
	tasks := make([]TaskSnapshot, 0, len(r.tasks))
	for _, trec := range r.tasks {
		tasks = append(tasks, trec.snapshot.Clone())
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	snap.Tasks = tasks

	// Update run status based on task statuses
	if len(tasks) > 0 && snap.Status != RunStatusCanceled && snap.Status != RunStatusFailed {
		allTerminal := true
		anyFailed := false
		anyCanceled := false
		for _, t := range tasks {
			if !isTerminalTaskStatus(t.Status) {
				allTerminal = false
				break
			}
			if t.Status == string(TaskStatusFailed) || t.Status == string(TaskStatusTimedOut) {
				anyFailed = true
			}
			if t.Status == string(TaskStatusCanceled) || t.Status == string(TaskStatusBlocked) {
				anyCanceled = true
			}
		}
		if allTerminal && len(tasks) > 0 {
			if anyFailed || anyCanceled {
				snap.Status = RunStatusFailed
			} else {
				snap.Status = RunStatusCompleted
			}
			if snap.CompletedAt == nil {
				t := now()
				snap.CompletedAt = &t
			}
		}
	}
	return snap
}

// updateRunStatusLocked recalculates the run status from task statuses.
// Must be called with m.mu held.
func (m *MemoryLedgerRepository) updateRunStatusLocked(rec *runRecord) {
	if len(rec.tasks) == 0 {
		return
	}
	allTerminal := true
	anyFailed := false
	anyCanceled := false
	for _, trec := range rec.tasks {
		if !isTerminalTaskStatus(trec.snapshot.Status) {
			allTerminal = false
			break
		}
		if trec.snapshot.Status == string(TaskStatusFailed) || trec.snapshot.Status == string(TaskStatusTimedOut) {
			anyFailed = true
		}
		if trec.snapshot.Status == string(TaskStatusCanceled) || trec.snapshot.Status == string(TaskStatusBlocked) {
			anyCanceled = true
		}
	}
	if allTerminal {
		if anyFailed || anyCanceled {
			rec.snapshot.Status = RunStatusFailed
		} else {
			rec.snapshot.Status = RunStatusCompleted
		}
		if rec.snapshot.CompletedAt == nil {
			t := m.now()
			rec.snapshot.CompletedAt = &t
		}
	}
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case string(TaskStatusCompleted), string(TaskStatusFailed),
		string(TaskStatusTimedOut), string(TaskStatusCanceled),
		string(TaskStatusBlocked):
		return true
	default:
		return false
	}
}
