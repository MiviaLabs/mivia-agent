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
		// A 409 ended the remote session. There is nothing left to push, so
		// exit without a final flush rather than replay into a dead session.
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

	_, err := FlushOutbox(ctx, s.client, s.outbox, sessionID)
	if err == nil {
		s.retryBase = 0
		s.retryAt = time.Time{}
		return
	}
	// ErrConflict: the server ended this session. ErrAuthStop: the settled
	// 401 policy - ErrReauthRequired / ErrSessionLost cannot be recovered
	// without `mivia login`, which this path must never prompt for. Both are
	// terminal for sync and neither touches the local chat.
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrAuthStop) {
		s.handleRemoteEnd(ctx)
		return
	}
	s.retryBase = nextRetryBackoff(s.retryBase)
	s.retryAt = time.Now().Add(jitterBackoff(s.retryBase))
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

// handleRemoteEnd latches the remote-ended state and shuts the pusher, the
// poller and the heartbeat down. It never creates a replacement session: a 409
// on append means the server ended this session, which is terminal for sync.
// The local chat is untouched.
//
// It is called from the worker goroutine, so the (blocking) runner stops are
// detached. Both runner Stop methods are idempotent, so a later Stop(ctx) that
// races this one is safe.
func (s *SyncSession) handleRemoteEnd(ctx context.Context) {
	if !s.remoteEnded.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if s.heartbeat != nil {
			s.heartbeat.Stop(ctx)
		}
		if s.poller != nil {
			s.poller.Stop(ctx)
		}
	}()
}

// applyForkedAttach re-bases the outbox onto a session that AttachSession
// forked because a foreign writer owned the old one, and records a
// sync.forked marker so a viewer can follow the new session.
func (s *SyncSession) applyForkedAttach() error {
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
	})
	if err := s.appendLocked([]WireEvent{we}); err != nil {
		return fmt.Errorf("append fork marker: %w", err)
	}
	return nil
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
		_ = s.appendLocked(wireEvents)
	}
	s.mu.Unlock()

	s.flushNow(ctx)
}
