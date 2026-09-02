package chatsync

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

func (s *SyncSession) workerLoop(ctx context.Context) {
	defer close(s.doneCh)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// A terminal stop latched. There is nothing left to push, so exit
		// without a final flush rather than replay into a dead session.
		if s.remoteEnded.Load() {
			return
		}
		select {
		case <-s.stopCh:
			s.drainAndFlushFinal(s.shutdownCtx(ctx))
			return
		case <-ctx.Done():
			s.drainAndFlushFinal(ctx)
			return
		case ev := <-s.eventCh:
			s.processEvent(ctx, ev)
		case <-s.flushCh:
			s.flush(ctx)
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

const (
	// flushRetryMinBackoff and flushRetryMaxBackoff are the settled jittered
	// retry bounds for a transient push failure (network, 5xx, 429):
	// 250ms -> 30s, chat-sync-cli-slice.md:194.
	flushRetryMinBackoff = 250 * time.Millisecond
	flushRetryMaxBackoff = 30 * time.Second
)

// flush pushes the outbox when the retry schedule allows it.
//
// Retrying a failing batch on the flush ticker resubmits an identical failing
// body at the ticker rate for as long as the failure lasts, which is a retry
// storm aimed at a server that is already in trouble. The batch stays at the
// outbox head either way - the wait is the whole point.
func (s *SyncSession) flush(ctx context.Context) {
	if !s.retryAt.IsZero() && time.Now().Before(s.retryAt) {
		return
	}
	s.flushNow(ctx)
}

// flushNow pushes the outbox regardless of the retry schedule. Shutdown uses
// it: a pending backoff must not silently discard the final flush.
func (s *SyncSession) flushNow(ctx context.Context) {
	if s.remoteEnded.Load() {
		return
	}
	sessionID := s.SessionID()

	moved, err := FlushOutbox(ctx, s.client, s.outbox, sessionID)
	if err == nil {
		s.retryBase = 0
		s.retryAt = time.Time{}
		s.lastGapBase = noGapBase
		if moved > 0 {
			// A recovery that was followed by a real push made progress;
			// only the empty-outbox (0, nil) answer leaves the count alone.
			s.consecutiveNoProgressRecoveries = 0
		}
		s.reportHealth(s.health.noteSuccess(s.outbox.UnflushedCount()), "")
		return
	}
	switch classifyFlushError(err) {
	case outcomeStop:
		s.stopTerminally(ctx, err)
	case outcomeRecover:
		s.recoverRemoteSession(ctx, err)
	default:
		// The sequence-gap 400 keeps its rebase path; everything else is
		// the jittered retry, and a 5xx must never fork.
		if errors.Is(err, ErrBadRequest) {
			s.handleBadRequest(ctx, err)
			return
		}
		s.scheduleRetry()
		s.reportHealth(s.health.noteFailure(err, s.outbox.UnflushedCount()), err.Error())
	}
}

// stopTerminally latches an outcomeStop with the wording each cause has
// always had: the settled 401 policy for auth, the poison rule for a body the
// server refused.
func (s *SyncSession) stopTerminally(ctx context.Context, err error) {
	if errors.Is(err, ErrAuthStop) {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: %v", err))
		return
	}
	s.poison(ctx, err)
}

// nextRetryBackoff doubles the undithered base and saturates at the ceiling.
// The base is kept undithered on purpose: doubling an already-jittered value
// compounds the jitter and drifts the real ceiling.
func nextRetryBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return flushRetryMinBackoff
	}
	if cur >= flushRetryMaxBackoff/2 {
		return flushRetryMaxBackoff
	}
	return cur * 2
}

// jitterBackoff spreads a delay over [d/2, d] so a fleet of clients that lost
// the same server does not return in lockstep.
func jitterBackoff(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// handleRemoteEnd latches the terminal state and shuts the pusher, the poller
// and the heartbeat down. It is reached only through classifyFlushError's
// outcomeStop, the recovery bounds, or a dead outbox; a 409 or 404 on its own
// recovers instead (recoverRemoteSession). The local chat is untouched.
//
// It is called from the worker goroutine, so the (blocking) runner stops are
// detached. Both runner Stop methods are idempotent, so a later Stop(ctx) that
// races this one is safe.
func (s *SyncSession) handleRemoteEnd(ctx context.Context, reason string) {
	if !s.remoteEnded.CompareAndSwap(false, true) {
		return
	}
	s.stopReason.Store(&reason)
	s.health.noteStop(reason, s.outbox.UnflushedCount())
	go func() {
		if s.heartbeat != nil {
			s.heartbeat.Stop(ctx)
		}
		if s.poller != nil {
			s.poller.Stop(ctx)
		}
		// The "say so" half of the contract's poison rule. The
		// CompareAndSwap above makes this exactly-once, and running it here
		// - already off the worker - keeps a host callback that blocks on a
		// terminal or a full UI channel away from the drain and final flush.
		if s.opts.OnStop != nil {
			s.opts.OnStop(reason)
		}
	}()
}

// reportHealth fires the host callback for a health transition, detached
// from the worker for the same reason OnStop is: a host that blocks on a
// terminal or a full UI channel must not hold up the drain and final flush.
func (s *SyncSession) reportHealth(transition, reason string) {
	switch transition {
	case SyncStateDegraded:
		if s.opts.OnDegraded != nil {
			go s.opts.OnDegraded(reason)
		}
	case SyncStateRecovered:
		if s.opts.OnRecovered != nil {
			go s.opts.OnRecovered()
		}
	}
}

func (s *SyncSession) drainAndFlushFinal(ctx context.Context) {
	for {
		select {
		case ev := <-s.eventCh:
			s.processEvent(ctx, ev)
		default:
			goto drained
		}
	}
drained:
	s.mu.Lock()
	wireEvents := s.projector.Flush(s.currentDrops())
	if len(wireEvents) > 0 {
		// Deliberately unreported, unlike the append hop in processEvent.
		// This is the final drop marker on a session that is shutting down:
		// the counters are process-local, so a loss recorded here reaches no
		// reader before the process exits. appendLocked still rolls the
		// watermark back, so nothing is double-counted if the outbox is
		// re-opened by a later run.
		_ = s.appendLocked(wireEvents)
	}
	s.mu.Unlock()

	s.flushNow(ctx)
}
