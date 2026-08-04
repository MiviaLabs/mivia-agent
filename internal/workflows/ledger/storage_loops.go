package ledger

import (
	"context"
	"fmt"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// IncrementLoopCounter mints the next iteration number for a named loop
// under the run claim, after catch-up. Counters are derived state: the
// returned number is persisted via a wf_loop_incremented event and rebuilt
// on reopen. Returns ErrNotFound if the run is absent.
func (s *StorageRepository) IncrementLoopCounter(ctx context.Context, runID, loopName string) (int, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return 0, err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return 0, ErrNotFound
	}
	next := 1
	for _, lc := range p.LoopCounters {
		if lc.RunID == runID && lc.LoopName == loopName && lc.Iterations >= next {
			next = lc.Iterations + 1
		}
	}
	now := s.now()
	prevCounters := append([]LoopCounter(nil), p.LoopCounters...)
	found := false
	for i := range p.LoopCounters {
		if p.LoopCounters[i].RunID == runID && p.LoopCounters[i].LoopName == loopName {
			p.LoopCounters[i].Iterations = next
			found = true
			break
		}
	}
	if !found {
		p.LoopCounters = append(p.LoopCounters, LoopCounter{RunID: runID, LoopName: loopName, Iterations: next})
	}
	s.proj[runID] = p
	s.mu.Unlock()

	payload, err := marshalLoopIncremented(loopIncrementedPayload{LoopName: loopName, Iterations: next, CreatedAt: now})
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, func() {
			q := s.proj[runID]
			q.LoopCounters = prevCounters
			s.proj[runID] = q
		})
		return 0, fmt.Errorf("marshal %s payload: %w", eventKindLoopIncremented, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindLoopIncremented, loopName, strconv.Itoa(next)),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindLoopIncremented,
		Payload:  payload,
	}
	return next, s.appendEvent(ctx, evt, func() {
		q := s.proj[runID]
		q.LoopCounters = prevCounters
		s.proj[runID] = q
	})
}

// GetLoopCounters returns the run's derived loop counters.
func (s *StorageRepository) GetLoopCounters(ctx context.Context, runID string) ([]LoopCounter, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	out := make([]LoopCounter, len(p.LoopCounters))
	copy(out, p.LoopCounters)
	return out, nil
}
