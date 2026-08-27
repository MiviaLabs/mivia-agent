package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// SetStepAttemptHeartbeat durably records one liveness observation for a
// RUNNING attempt. Each call appends ONE wf_attempt_heartbeat event whose
// deterministic event ID embeds the heartbeat timestamp, so successive ticks
// (distinct HeartbeatAt) append DISTINCT events and a later tick can never
// collide with an earlier one (replay-safe: no ErrConflict on later ticks).
// The payload is byte-identical for a given HeartbeatAt (CreatedAt mirrors
// it), so a retried append of the SAME heartbeat dedupes on the event ID and
// returns nil (idempotent), never ErrConflict. The attempt's status/version
// are never changed and no step candidate is contributed, exactly like the
// replay. Returns ErrNotFound if the run or attempt is absent.
func (s *StorageRepository) SetStepAttemptHeartbeat(ctx context.Context, runID, attemptID string, heartbeatAt time.Time) error {
	if attemptID == "" {
		return fmt.Errorf("attempt id is empty")
	}
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	idx := -1
	for i := range p.Attempts {
		if p.Attempts[i].AttemptID == attemptID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrNotFound
	}
	if IsTerminalAttemptStatus(p.Attempts[idx].Status) {
		// A heartbeat can arrive after the attempt already settled (e.g. a
		// watchdog tick racing the run's terminal transition). Silently drop
		// it: writing liveness data onto a closed attempt would corrupt its
		// audit trail for no benefit.
		s.mu.Unlock()
		return nil
	}
	prev := p.Attempts[idx].LastHeartbeatAt
	if p.Attempts[idx].LastHeartbeatAt.IsZero() || !heartbeatAt.Before(p.Attempts[idx].LastHeartbeatAt) {
		p.Attempts[idx].LastHeartbeatAt = heartbeatAt
	}
	s.proj[runID] = p
	s.mu.Unlock()

	payload, err := marshalAttemptHeartbeat(attemptHeartbeatPayload{
		AttemptID:   attemptID,
		HeartbeatAt: heartbeatAt,
		CreatedAt:   heartbeatAt,
	})
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, func() {
			q := s.proj[runID]
			if idx < len(q.Attempts) {
				q.Attempts[idx].LastHeartbeatAt = prev
			}
			s.proj[runID] = q
		})
		return fmt.Errorf("marshal %s payload: %w", eventKindAttemptHeartbeat, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindAttemptHeartbeat, attemptID, heartbeatAt.UTC().Format(time.RFC3339Nano)),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindAttemptHeartbeat,
		Payload:  payload,
	}
	rollback := func() {
		q := s.proj[runID]
		if idx < len(q.Attempts) {
			q.Attempts[idx].LastHeartbeatAt = prev
		}
		s.proj[runID] = q
	}
	return s.appendEvent(ctx, evt, rollback)
}
