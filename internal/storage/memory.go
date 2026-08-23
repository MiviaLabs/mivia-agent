package storage

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

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
	// fencedTokens records every holder a run has ever ejected (token → nothing,
	// per-run set). The history is durable across releases so a re-issued
	// claim by a previously fenced token reads true until cleanup. The
	// current holder is, by definition, never in this set.
	fencedTokens map[string]map[string]struct{} // runID → set of tokens
	// content maps content-addressed references to raw bytes.
	content map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{
		events:       map[string][]Event{},
		ids:          map[string]struct{}{},
		maxSeq:       map[string]int{},
		claims:       map[string]Claim{},
		fencedTokens: map[string]map[string]struct{}{},
		content:      map[string][]byte{},
	}
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
	// A deletion tombstone alone does not make the run live again: only a
	// surviving non-tombstone event (or claim) refuses admission. The claim
	// probe and appendBatchLocked's duplicate/empty-payload checks are
	// unchanged, so the whole gate stays one lock section.
	for _, e := range m.events[runID] {
		if e.Kind != KindRunDeleted {
			return ErrDuplicate
		}
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
	m.deleteRunLocked(runID, throughSequence)
	return nil
}

// AppendAndDeleteRun appends a deletion tombstone and deletes earlier events
// and the claim while holding one lock.
func (m *Memory) AppendAndDeleteRun(_ context.Context, tombstone Event, claim Claim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.claims[tombstone.RunID]; ok &&
		(claim.Holder == "" || current.Holder != claim.Holder ||
			(claim.Fence != 0 && current.Fence != claim.Fence)) {
		return ErrClaimHeld
	}
	if err := m.appendLocked(tombstone); err != nil {
		return err
	}
	m.deleteRunLocked(tombstone.RunID, tombstone.Sequence-1)
	return nil
}

func (m *Memory) deleteRunLocked(runID string, throughSequence int) {
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
	existing, ok := m.claims[runID]
	fence := existing.Fence + 1
	if !ok {
		fence = 1
	}
	if ok && existing.Holder != holder {
		m.recordFencedLocked(runID, existing.Holder)
	}
	m.claims[runID] = Claim{RunID: runID, Holder: holder, AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano), Fence: fence}
	return nil
}

func (m *Memory) TakeoverClaimFenced(_ context.Context, runID, holder string) (Claim, error) {
	if holder == "" {
		return Claim{}, ErrClaimNotHeld
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	fence := existing.Fence + 1
	if !ok {
		fence = 1
	}
	if ok && existing.Holder != holder {
		m.recordFencedLocked(runID, existing.Holder)
	}
	claim := Claim{RunID: runID, Holder: holder, AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano), Fence: fence}
	m.claims[runID] = claim
	return claim, nil
}

// recordFencedLocked adds token to the run's fenced-token set. Caller must
// hold m.mu.
func (m *Memory) recordFencedLocked(runID, token string) {
	set, ok := m.fencedTokens[runID]
	if !ok {
		set = map[string]struct{}{}
		m.fencedTokens[runID] = set
	}
	set[token] = struct{}{}
}

// RefreshClaimFenced refreshes the claim's acquired_at ONLY when holder already
// owns the claim row. A missing row (or a row owned by another holder) returns
// ErrClaimNotHeld, so a heartbeat can never insert itself back into a claim it
// lost (F2).
func (m *Memory) RefreshClaimFenced(_ context.Context, runID, holder string) (Claim, error) {
	if holder == "" {
		return Claim{}, ErrClaimNotHeld
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if !ok || existing.Holder != holder {
		return Claim{}, ErrClaimNotHeld
	}
	existing.AcquiredAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.claims[runID] = existing
	return existing, nil
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
	if existing.Holder != holder {
		m.recordFencedLocked(runID, existing.Holder)
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

func (m *Memory) GetClaim(_ context.Context, runID string) (Claim, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	claim, ok := m.claims[runID]
	if !ok {
		return Claim{}, ErrClaimNotHeld
	}
	return claim, nil
}

// IsRunHeld reports whether runID currently has an active claim. A pure
// liveness probe; it never acquires, refreshes, or releases a claim.
func (m *Memory) IsRunHeld(_ context.Context, runID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.claims[runID]
	return ok, nil
}

// IsRunTokenFenced reports whether token has been fenced out of runID by a
// subsequent takeover. The history is durable across releases. A token that
// is the current holder of runID always reads false.
func (m *Memory) IsRunTokenFenced(_ context.Context, runID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if claim, ok := m.claims[runID]; ok && claim.Holder == token {
		return false, nil
	}
	set, ok := m.fencedTokens[runID]
	if !ok {
		return false, nil
	}
	_, fenced := set[token]
	return fenced, nil
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
