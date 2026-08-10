package ledger

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxReferenceBytes = 256
	maxEventPayload   = 1024
)

// MemoryLedgerRepository is an in-memory implementation of LedgerRepository.
// It uses sync.RWMutex for concurrency safety and returns defensive copies
// on all read paths. It is the default backend for Phase 1 and suitable for
// unit and race tests.
type MemoryLedgerRepository struct {
	mu         sync.RWMutex
	runs       map[string]*runRecord
	idemLookup map[string]string      // idempotency key → runID (for O(1) dedup)
	claims     map[string]memoryClaim // runID → claim
	content    map[string][]byte      // ref → raw bytes for content-addressed storage
	closed     bool
	now        func() time.Time // injectable time source for tests
}

type memoryClaim struct {
	holder string
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
		runs:       map[string]*runRecord{},
		idemLookup: map[string]string{},
		claims:     map[string]memoryClaim{},
		content:    map[string][]byte{},
		now:        time.Now,
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
		// O(1) idempotency key lookup via top-level index.
		if _, ok := m.idemLookup[key]; ok {
			return ErrDuplicate
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
		m.idemLookup[key] = snapshot.RunID
	}
	// Stamp only what arrives unstamped. The caller's CreatedAt is data, not a
	// suggestion: the coordinator sets a real one before calling, the storage
	// backend marshals that value into the durable run_created payload, and
	// replay hands it back here. Overwriting it unconditionally made every
	// recovered run report the replay instant as its start time, which
	// `mivia diagnostics` then turned into an Elapsed of a few milliseconds.
	if rec.snapshot.CreatedAt.IsZero() {
		rec.snapshot.CreatedAt = m.now()
	}
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

func (m *MemoryLedgerRepository) GetRunByIdempotencyKey(_ context.Context, key string) (RunSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if key == "" {
		return RunSnapshot{}, ErrNotFound
	}
	if runID, ok := m.idemLookup[key]; ok {
		if rec, ok := m.runs[runID]; ok {
			return rec.fullSnapshot(m.now), nil
		}
	}
	return RunSnapshot{}, ErrNotFound
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
	if len(event.Payload) > maxEventPayload {
		return fmt.Errorf("event payload exceeds %d bytes", maxEventPayload)
	}
	// Check duplicate by event ID
	for _, ev := range rec.events {
		if ev.ID == event.ID {
			return ErrDuplicate
		}
	}
	seq := rec.sequences[event.RunID] + 1
	event.Sequence = seq
	// Sequence is always derived here - it numbers this projection's own view of
	// the run. CreatedAt is not: a non-zero value was set by whoever knows when
	// the event happened (the storage backend before it marshalled the durable
	// copy, or the replay path decoding that copy back out), and re-stamping it
	// would report the read instant as the event instant.
	if event.CreatedAt.IsZero() {
		event.CreatedAt = m.now()
	}
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
	trec.snapshot.OutputRef = normalizeReference(outputRef)
	trec.snapshot.ErrorRef = normalizeReference(errorRef)
	return nil
}

func (m *MemoryLedgerRepository) SetTaskAttempt(_ context.Context, runID, taskID, attemptID, status string, finishedAt *time.Time) error {
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
	for i := range trec.snapshot.Attempts {
		if trec.snapshot.Attempts[i].AttemptID != attemptID {
			continue
		}
		trec.snapshot.Attempts[i].Status = status
		if finishedAt != nil {
			t := *finishedAt
			trec.snapshot.Attempts[i].FinishedAt = &t
		}
		return nil
	}
	// An unknown attempt ID starts a new attempt. StorageLedgerRepository has
	// always appended here; returning ErrNotFound instead meant the two
	// repositories disagreed on the contract, and a resumed execution recording
	// a fresh attempt silently failed on memory while succeeding on SQLite.
	att := AttemptSnapshot{
		AttemptID:  attemptID,
		TaskID:     taskID,
		RunID:      runID,
		AttemptNum: len(trec.snapshot.Attempts) + 1,
		Status:     status,
	}
	if finishedAt != nil {
		t := *finishedAt
		att.FinishedAt = &t
	}
	trec.snapshot.Attempts = append(trec.snapshot.Attempts, att)
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
	rec, ok := m.runs[runID]
	if !ok {
		return ErrNotFound
	}
	// Clean up idempotency key index.
	for key := range rec.idemKeys {
		delete(m.idemLookup, key)
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

	// Derive run status from task statuses.
	if len(tasks) > 0 {
		snap.Status = deriveRunStatus(tasks)
	}
	if isRunTerminal(snap.Status) && snap.CompletedAt == nil {
		t := now()
		snap.CompletedAt = &t
	}
	return snap
}

// deriveRunStatus determines the run-level status from task statuses.
func deriveRunStatus(tasks []TaskSnapshot) RunStatus {
	hasQueued := false
	hasRunning := false
	allTerminal := true
	anyFailedOrTimedOut := false
	anyCanceled := false
	anyCompleted := false

	for _, t := range tasks {
		switch t.Status {
		case string(TaskStatusQueued):
			hasQueued = true
			allTerminal = false
		case string(TaskStatusRunning), string(TaskStatusCancelRequested):
			hasRunning = true
			allTerminal = false
		case string(TaskStatusCompleted):
			anyCompleted = true
		case string(TaskStatusFailed), string(TaskStatusTimedOut):
			anyFailedOrTimedOut = true
		case string(TaskStatusCanceled), string(TaskStatusBlocked):
			anyCanceled = true
		default:
			allTerminal = false
		}
	}

	if !allTerminal {
		if hasRunning {
			return RunStatusRunning
		}
		if hasQueued && !hasRunning {
			return RunStatusQueued
		}
		return RunStatusRunning
	}

	// All tasks are terminal.
	if anyFailedOrTimedOut {
		return RunStatusFailed
	}
	if anyCanceled {
		return RunStatusCanceled
	}
	if anyCompleted {
		return RunStatusCompleted
	}
	return RunStatusFailed
}

// isRunTerminal returns true if the run status is terminal.
func isRunTerminal(s RunStatus) bool {
	return s == RunStatusCompleted || s == RunStatusFailed || s == RunStatusCanceled
}

// updateRunStatusLocked recalculates the run status from task statuses.
// Must be called with m.mu held.
func (m *MemoryLedgerRepository) updateRunStatusLocked(rec *runRecord) {
	if len(rec.tasks) == 0 {
		return
	}
	tasks := make([]TaskSnapshot, 0, len(rec.tasks))
	for _, trec := range rec.tasks {
		tasks = append(tasks, trec.snapshot)
	}
	rec.snapshot.Status = deriveRunStatus(tasks)
	if isRunTerminal(rec.snapshot.Status) && rec.snapshot.CompletedAt == nil {
		t := m.now()
		rec.snapshot.CompletedAt = &t
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

func normalizeReference(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if len(ref) <= maxReferenceBytes {
		return ref
	}
	digest := sha256.Sum256([]byte(ref))
	return fmt.Sprintf("ref:sha256:%x", digest[:])
}
