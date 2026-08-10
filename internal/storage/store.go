// Package storage provides the validation seam for durable agent events.
package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrDuplicate       = errors.New("duplicate event")
	ErrClaimHeld       = errors.New("run claim held by another holder")
	ErrClaimNotHeld    = errors.New("run claim not held by this holder")
	ErrContentNotFound = errors.New("content not found")
)

// Claim represents an exclusive execution claim on a run.
type Claim struct {
	RunID      string
	Holder     string
	AcquiredAt string
	Fence      uint64
}

type Event struct {
	ID       string
	RunID    string
	Sequence int
	Kind     string
	Payload  []byte
	// RowID is the event's position in the store's global append order: the
	// SQLite rowid, or the monotone append index on the memory backend. It is
	// set by the store when events are read so a reader can fold events from
	// several runs in the order they were actually appended - in particular a
	// run_deleted tombstone always precedes a later run_created that reuses
	// its idempotency key.
	RowID uint64
}

type Store interface {
	Append(context.Context, Event) error
	// AppendClaimed appends an event when its run is unclaimed or holder owns
	// the current claim. It returns ErrClaimHeld when another holder owns it.
	AppendClaimed(ctx context.Context, event Event, holder string) error
	Events(context.Context, string) ([]Event, error)
	// EventsSince returns the events of a run whose sequence is strictly
	// greater than afterSequence, ordered by ascending sequence. It is the
	// bounded tail read that lets a reader catch up on another writer's
	// appends without replaying the whole history.
	EventsSince(ctx context.Context, runID string, afterSequence int) ([]Event, error)
	// DeleteRun removes events at or below throughSequence and any claim for a
	// run. It never deletes content; a later tombstone event remains visible.
	DeleteRun(ctx context.Context, runID string, throughSequence int) error
	// Changes is the freshness probe for incremental catch-up. Given a cursor
	// previously returned by Changes (0 to start from the beginning), it
	// reports the highest sequence of every run appended to since that cursor,
	// together with the new cursor. Cost is proportional to the number of runs
	// that moved, not to the size of the history, so a caller that is already
	// up to date pays a constant-time probe.
	Changes(ctx context.Context, afterCursor uint64) (maxSequences map[string]int, cursor uint64, err error)
	// ClaimRun acquires an exclusive claim on a run for holder. Returns nil
	// if the claim was acquired. Returns ErrClaimHeld if another holder
	// already holds the claim. The same holder calling ClaimRun again
	// refreshes the claim successfully.
	ClaimRun(ctx context.Context, runID, holder string) error
	// TakeoverClaim atomically replaces any existing claim with holder.
	TakeoverClaim(ctx context.Context, runID, holder string) error
	// ReleaseClaim releases the claim on a run. Only the current holder may
	// release. Returns ErrClaimNotHeld if the caller does not hold the claim.
	ReleaseClaim(ctx context.Context, runID, holder string) error
	// ClearClaim force-releases any claim on a run, regardless of holder.
	// Returns nil if no claim existed. Used during crash recovery to clear
	// stale claims on terminal runs.
	ClearClaim(ctx context.Context, runID string) error
	// PutContent stores raw bytes keyed by a content-addressed reference
	// (e.g. "ref:output:xxxx"). Idempotent for the same ref.
	PutContent(ctx context.Context, ref string, data []byte) error
	// GetContent retrieves bytes previously stored by PutContent.
	// Returns ErrContentNotFound if the ref is unknown.
	GetContent(ctx context.Context, ref string) ([]byte, error)
	Count(context.Context) (int, error)
	ListRunIDs(context.Context) ([]string, error)
	Close() error
}

// ExistingClaimAppender appends only when holder owns an existing claim.
// Unlike Store.AppendClaimed, an unclaimed run is refused.
type ExistingClaimAppender interface {
	AppendWithExistingClaim(context.Context, Event, string) error
}

// BatchAppender atomically appends a set of events.
type BatchAppender interface {
	AppendBatch(context.Context, []Event) error
}

// NewRunBatchAppender atomically appends a new-run batch only when no event
// or claim exists for its run. It is the atomic admission boundary.
type NewRunBatchAppender interface {
	AppendBatchForNewRun(context.Context, string, []Event) error
}

// LeaseStore is the optional extension used by workflow recovery.
type LeaseStore interface {
	TakeoverExpiredClaim(context.Context, string, string, time.Duration) error
}

// FencedLeaseStore guards stale writes after an expired claim changes owner.
type FencedLeaseStore interface {
	ClaimRunFenced(context.Context, string, string) (Claim, error)
	TakeoverExpiredClaimFenced(context.Context, string, string, time.Duration) (Claim, error)
	AppendClaimedFenced(context.Context, Event, Claim) error
	ReleaseClaimFenced(context.Context, Claim) error
}

type Memory struct {
	mu     sync.RWMutex
	events map[string][]Event
	ids    map[string]struct{}
	// order records the run ID of each append in order, so Changes can report
	// what moved since a cursor without scanning the whole history. The cursor
	// is an index into this slice.
	order  []string
	maxSeq map[string]int
	// claims tracks exclusive run execution claims.
	claims map[string]Claim // runID → claim
	// content maps content-addressed references to raw bytes.
	content map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{events: map[string][]Event{}, ids: map[string]struct{}{}, maxSeq: map[string]int{}, claims: map[string]Claim{}, content: map[string][]byte{}}
}

func (m *Memory) Append(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendLocked(e)
}

func (m *Memory) AppendBatch(_ context.Context, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendBatchLocked(events)
}

func (m *Memory) AppendBatchForNewRun(_ context.Context, runID string, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[runID]) != 0 {
		return ErrDuplicate
	}
	if _, claimed := m.claims[runID]; claimed {
		return ErrClaimHeld
	}
	return m.appendBatchLocked(events)
}

func (m *Memory) appendBatchLocked(events []Event) error {
	seenIDs := make(map[string]struct{}, len(events))
	seenSeq := make(map[string]map[int]struct{}, len(events))
	for _, e := range events {
		if _, ok := m.ids[e.ID]; ok {
			return ErrDuplicate
		}
		if _, ok := seenIDs[e.ID]; ok {
			return ErrDuplicate
		}
		seenIDs[e.ID] = struct{}{}
		if e.Sequence <= m.maxSeq[e.RunID] {
			return ErrDuplicate
		}
		if seenSeq[e.RunID] == nil {
			seenSeq[e.RunID] = make(map[int]struct{})
		}
		if _, ok := seenSeq[e.RunID][e.Sequence]; ok {
			return ErrDuplicate
		}
		seenSeq[e.RunID][e.Sequence] = struct{}{}
		if len(e.Payload) == 0 {
			return fmt.Errorf("empty payload")
		}
	}
	for _, e := range events {
		if err := m.appendLocked(e); err != nil {
			return err
		}
	}
	return nil
}

// AppendClaimed atomically checks the run claim and appends the event.
func (m *Memory) AppendClaimed(_ context.Context, e Event, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if claim, ok := m.claims[e.RunID]; ok && (holder == "" || claim.Holder != holder) {
		return ErrClaimHeld
	}
	return m.appendLocked(e)
}

func (m *Memory) AppendWithExistingClaim(_ context.Context, e Event, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	claim, ok := m.claims[e.RunID]
	if !ok || holder == "" || claim.Holder != holder {
		return ErrClaimHeld
	}
	return m.appendLocked(e)
}

func (m *Memory) appendLocked(e Event) error {
	if _, ok := m.ids[e.ID]; ok {
		return ErrDuplicate
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	// RowID mirrors the SQLite rowid semantics: a fresh monotone index over
	// the append order, never reused after DeleteRun. m.order never shrinks,
	// so len(m.order)+1 is strictly increasing for every append.
	e.RowID = uint64(len(m.order) + 1)
	m.events[e.RunID] = append(m.events[e.RunID], cloneEvent(e))
	m.ids[e.ID] = struct{}{}
	m.order = append(m.order, e.RunID)
	if e.Sequence > m.maxSeq[e.RunID] {
		m.maxSeq[e.RunID] = e.Sequence
	}
	return nil
}

func (m *Memory) Events(_ context.Context, runID string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Event, len(m.events[runID]))
	for i, e := range m.events[runID] {
		out[i] = cloneEvent(e)
	}
	return out, nil
}

func (m *Memory) EventsSince(_ context.Context, runID string, afterSequence int) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	for _, e := range m.events[runID] {
		if e.Sequence > afterSequence {
			out = append(out, cloneEvent(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

func (m *Memory) DeleteRun(_ context.Context, runID string, throughSequence int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.events[runID]
	kept := events[:0]
	for _, event := range events {
		if event.Sequence <= throughSequence {
			delete(m.ids, event.ID)
			continue
		}
		kept = append(kept, event)
	}
	if len(kept) == 0 {
		delete(m.events, runID)
		delete(m.maxSeq, runID)
	} else {
		m.events[runID] = kept
		m.maxSeq[runID] = kept[len(kept)-1].Sequence
	}
	delete(m.claims, runID)
	return nil
}

func (m *Memory) Changes(_ context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cursor := uint64(len(m.order))
	if afterCursor >= cursor {
		return nil, cursor, nil
	}
	out := map[string]int{}
	for _, runID := range m.order[afterCursor:] {
		if _, seen := out[runID]; seen {
			continue
		}
		out[runID] = m.maxSeq[runID]
	}
	return out, cursor, nil
}

func (m *Memory) Count(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ids), nil
}

func (m *Memory) ListRunIDs(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.events))
	for id := range m.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) ClaimRun(_ context.Context, runID, holder string) error {
	_, err := m.ClaimRunFenced(context.Background(), runID, holder)
	return err
}

func (m *Memory) ClaimRunFenced(_ context.Context, runID, holder string) (Claim, error) {
	if holder == "" {
		return Claim{}, ErrClaimNotHeld
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if ok && existing.Holder != holder {
		return Claim{}, ErrClaimHeld
	}
	if existing.Fence == 0 {
		existing.Fence = 1
	}
	if !ok {
		existing = Claim{RunID: runID, Holder: holder, Fence: 1}
	}
	existing.AcquiredAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.claims[runID] = existing
	return existing, nil
}

func (m *Memory) TakeoverClaim(_ context.Context, runID, holder string) error {
	if holder == "" {
		return ErrClaimNotHeld
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claims[runID] = Claim{RunID: runID, Holder: holder, AcquiredAt: time.Now().UTC().Format(time.RFC3339)}
	return nil
}

func (m *Memory) TakeoverExpiredClaim(_ context.Context, runID, holder string, maxAge time.Duration) error {
	_, err := m.TakeoverExpiredClaimFenced(context.Background(), runID, holder, maxAge)
	return err
}

func (m *Memory) TakeoverExpiredClaimFenced(_ context.Context, runID, holder string, maxAge time.Duration) (Claim, error) {
	if holder == "" {
		return Claim{}, ErrClaimNotHeld
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if !ok {
		return Claim{}, ErrClaimNotHeld
	}
	if ok {
		when, err := time.Parse(time.RFC3339Nano, existing.AcquiredAt)
		if err != nil || time.Since(when) < maxAge {
			return Claim{}, ErrClaimHeld
		}
	}
	existing.Holder = holder
	existing.Fence++
	if existing.Fence == 0 {
		existing.Fence = 1
	}
	existing.AcquiredAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.claims[runID] = existing
	return existing, nil
}

func (m *Memory) AppendClaimedFenced(_ context.Context, e Event, claim Claim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.claims[e.RunID]
	if !ok || current.Holder != claim.Holder || current.Fence != claim.Fence {
		return ErrClaimHeld
	}
	return m.appendLocked(e)
}

func (m *Memory) ReleaseClaimFenced(_ context.Context, claim Claim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.claims[claim.RunID]
	if !ok || current.Holder != claim.Holder || current.Fence != claim.Fence {
		return ErrClaimNotHeld
	}
	delete(m.claims, claim.RunID)
	return nil
}

func (m *Memory) ReleaseClaim(_ context.Context, runID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if !ok {
		return ErrClaimNotHeld
	}
	if existing.Holder != holder {
		return ErrClaimNotHeld
	}
	delete(m.claims, runID)
	return nil
}

func (m *Memory) ClearClaim(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claims, runID)
	return nil
}

func (m *Memory) PutContent(_ context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content[ref] = cloneBytes(data)
	return nil
}

func (m *Memory) GetContent(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.content[ref]
	if !ok {
		return nil, ErrContentNotFound
	}
	return cloneBytes(data), nil
}

func cloneEvent(e Event) Event   { e.Payload = append([]byte(nil), e.Payload...); return e }
func cloneBytes(b []byte) []byte { out := make([]byte, len(b)); copy(out, b); return out }
