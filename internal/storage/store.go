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
}

type Event struct {
	ID       string
	RunID    string
	Sequence int
	Kind     string
	Payload  []byte
}

type Store interface {
	Append(context.Context, Event) error
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
	if _, ok := m.ids[e.ID]; ok {
		return ErrDuplicate
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if ok && existing.Holder != holder {
		return ErrClaimHeld
	}
	m.claims[runID] = Claim{RunID: runID, Holder: holder, AcquiredAt: time.Now().UTC().Format(time.RFC3339)}
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
