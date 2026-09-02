package chatsync

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// recoveryInterval is the least time between two recoveries. A second
	// trigger inside it is DEFERRED, never refused for good: two web deletes
	// in one minute must not kill sync. recoveryIntervalVar is what the code
	// reads, so tests can shrink it (the defaultRequestTimeout precedent).
	recoveryInterval = 60 * time.Second
	// maxNoProgressRecoveries bounds recoveries that pushed nothing in
	// between. Two in a row prove the new session fails exactly like the old
	// one, and minting a third would be forking on the ticker.
	maxNoProgressRecoveries = 2
	// createFailuresBeforeThrottle is how many consecutive failed
	// CreateSession attempts engage the create throttle.
	createFailuresBeforeThrottle = 3
)

// createThrottlePeriod spaces create attempts once the throttle is engaged.
// It is a RATE bound, never a terminal one: the next throttled attempt
// succeeds as soon as the API is healthy again, with no restart. A var, not
// a const: tests shrink it so the ordering assertions do not wait five
// minutes (the defaultRequestTimeout precedent).
var createThrottlePeriod = 5 * time.Minute

// recoveryIntervalVar is recoveryInterval as read at runtime; see there.
var recoveryIntervalVar = recoveryInterval

// recoverRemoteSession abandons the current remote session and re-attaches
// the unflushed backlog onto a fresh one. It runs on the worker.
//
// Lock discipline, which is load-bearing:
//   - CreateSession runs OUTSIDE s.mu. An HTTP round trip must not hold a
//     mutex the bus handler path can contend on.
//   - s.mu is then taken ONCE for the whole mutation: the id swap and
//     rebaseOntoSessionLocked. Nothing inside calls SessionID() or LastSeq().
//   - The window between the create and the lock is unguarded, so a
//     handleRemoteEnd can land in it. Under the lock only remoteEnded is
//     re-checked; if set, the fresh session is abandoned without a rebase.
//     running is deliberately NOT checked: Stop clears it before the final
//     drain, and a running guard would abandon every recovery on the
//     shutdown path, at the moment the backlog is largest.
//   - Heartbeat, poller and the identity write-back run after the unlock;
//     each has its own lock and none re-enters SyncSession.
func (s *SyncSession) recoverRemoteSession(ctx context.Context, cause error) {
	now := time.Now()
	if s.outbox.Dead() {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: the outbox is no longer writable, so the backlog cannot be moved to a new session (cause: %v)", cause))
		return
	}
	if s.consecutiveNoProgressRecoveries >= maxNoProgressRecoveries {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: %d consecutive recoveries pushed nothing, the new session fails like the old one (cause: %v)",
			s.consecutiveNoProgressRecoveries, cause))
		return
	}
	// Interval and throttle refusals defer: no request, no counter moves.
	if !s.lastRecoveryAt.IsZero() && now.Sub(s.lastRecoveryAt) < recoveryIntervalVar {
		s.scheduleRetry()
		return
	}
	if now.Before(s.createThrottledUntil) {
		s.consecutiveCreateFailures++
		s.scheduleRetry()
		return
	}

	oldID := s.SessionID()
	created, err := s.client.CreateSession(ctx, s.createParams)
	if err != nil {
		s.handleCreateFailure(ctx, err)
		return
	}
	s.consecutiveCreateFailures = 0
	s.createThrottledUntil = time.Time{}
	if s.beforeRecoveryLock != nil {
		s.beforeRecoveryLock()
	}

	s.mu.Lock()
	if s.remoteEnded.Load() {
		s.mu.Unlock()
		return
	}
	s.sessionID = created.ID
	rebaseErr := s.rebaseOntoSessionLocked(oldID)
	s.mu.Unlock()
	if rebaseErr != nil {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: recovery onto session %s failed: %v (cause: %v)", created.ID, rebaseErr, cause))
		return
	}

	s.lastRecoveryAt = now
	s.consecutiveNoProgressRecoveries++
	s.health.noteRecovery()
	if s.heartbeat != nil {
		s.heartbeat.SetSessionID(created.ID)
	}
	if s.poller != nil {
		s.poller.SetSessionID(created.ID)
	}
	// Same posture as OpenSession: a write-back failure costs the NEXT run
	// its resume, not this one.
	_ = s.opts.persistRemoteSessionID(created.ID)

	s.retryBase = 0
	s.retryAt = time.Time{}
	s.lastGapBase = noGapBase
	// Push now rather than on the next tick: inside drainAndFlushFinal there
	// is no next tick, and a final-flush recovery is exactly the case where
	// losing the backlog is permanent. Re-entry is bounded by the interval
	// refusal above.
	s.flushNow(ctx)
}

// handleCreateFailure is the create-rejection policy. No client state has
// changed - the outbox is untouched, the rebase is strictly after a
// successful create - so retrying risks nothing locally. It never latches
// (except on a fatal auth failure), never counts as a recovery, and after
// createFailuresBeforeThrottle consecutive failures throttles further
// attempts to one per createThrottlePeriod. Every failure feeds the degraded
// transition so the retry is loud.
func (s *SyncSession) handleCreateFailure(ctx context.Context, err error) {
	if errors.Is(err, ErrAuthStop) {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: %v", err))
		return
	}
	s.consecutiveCreateFailures++
	if s.consecutiveCreateFailures >= createFailuresBeforeThrottle {
		s.createThrottledUntil = time.Now().Add(createThrottlePeriod)
	}
	s.scheduleRetry()
	s.reportHealth(s.health.noteCreateFailure(err, s.outbox.UnflushedCount(), s.consecutiveCreateFailures, s.createThrottledUntil), err.Error())
}

// rebaseOntoSessionLocked re-bases the outbox onto s.sessionID, which the
// caller has just set to a freshly created remote session, and records a
// sync.forked marker naming both sessions so a viewer can follow the move.
// Two callers: the foreign-writer fork at attach time and recovery.
//
// The caller must hold s.mu. This function calls only appendLocked, the
// outbox and the projector, and reads the s.sessionID field directly; it
// must never call SessionID(), LastSeq() or flushNow.
func (s *SyncSession) rebaseOntoSessionLocked(forkedFrom string) error {
	unflushedCount, err := s.outbox.ResetForFork()
	if err != nil {
		return fmt.Errorf("reset outbox for fork: %w", err)
	}
	s.projector.ResetSeq(int64(unflushedCount))

	we := s.projector.nextWireEvent(TypeSyncForked, &SyncForkedPayload{
		Envelope: Envelope{
			V:    1,
			At:   time.Now(),
			Turn: "synthetic:fork",
		},
		NewSessionID: s.sessionID,
		ForkedFrom:   forkedFrom,
	})
	if err := s.appendLocked([]WireEvent{we}); err != nil {
		return fmt.Errorf("append fork marker: %w", err)
	}
	return nil
}

// consecutiveCreateFailuresForTest exposes the create-failure counter to the
// package tests that pin the throttle's reset.
func (s *SyncSession) consecutiveCreateFailuresForTest() int { return s.consecutiveCreateFailures }
