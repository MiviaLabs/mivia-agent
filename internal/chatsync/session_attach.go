package chatsync

import (
	"context"
	"fmt"
)

// ensureAttached performs the deferred remote attach exactly once, on the
// worker goroutine, before the first event is projected.
//
// It is the only network work the worker ever does, and it exists because
// OpenSession must not touch the API: an event only exists once a turn
// starts, so gating the attach on the first event is what keeps a CLI that
// was opened but never used from creating a remote session, sending
// heartbeats, or long-polling. Everything the eager attach used to do at
// open happens here, in the same order: attach (create, or re-attach to the
// persisted remote session), align the projector's seq onto the server's
// mark, record the fork marker when the attach forked, write the identity
// back for the next run, then start the heartbeat and the poller - both are
// API traffic, so both belong to the attach, not to the open.
//
// While the attach's round trip runs, later events queue on eventCh: a
// delay at the start of the first turn, never a loss or a reorder.
//
// A failure latches the same terminal stop every other fatal condition uses
// - stop syncing and SAY SO, through OnStop - because the eager attach's
// answer to a failure (OpenSession returns an error, the host skips sync)
// is not available once sync is already armed and running.
func (s *SyncSession) ensureAttached(ctx context.Context) error {
	if s.attached.Load() {
		return nil
	}
	att, err := AttachSession(ctx, s.client, s.outbox, s.createParams, s.opts.RemoteSessionID, s.opts.ProjectorOptions.WriterID)
	if err != nil {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: attach failed: %v", err))
		return err
	}
	seq, err := openingSeq(s.outbox, att)
	if err != nil {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: %v", err))
		return err
	}

	s.mu.Lock()
	s.sessionID = att.SessionID
	s.projector.ResetSeq(seq)
	var forkErr error
	if att.ForkedFrom != "" {
		// One contract serves both callers of rebaseOntoSessionLocked: the
		// worker holds s.mu here exactly as the eager open did before any
		// worker existed.
		forkErr = s.rebaseOntoSessionLocked(att.ForkedFrom)
	}
	s.mu.Unlock()
	if forkErr != nil {
		s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped: apply forked attach: %v", forkErr))
		return forkErr
	}

	// Same posture as recovery and the eager open: a write-back failure
	// costs the NEXT run its resume, not this one.
	_ = s.opts.persistRemoteSessionID(att.SessionID)
	s.health.noteOpen(s.outbox.UnflushedCount())

	s.attached.Store(true)
	s.startRunners(ctx, att.SessionID)
	return nil
}

// startRunners launches the heartbeat and the input poller, which are part
// of the attach: both speak to a remote session, so both wait until one
// exists. Called from the worker after the attach succeeds.
func (s *SyncSession) startRunners(ctx context.Context, remoteID string) {
	s.runnersMu.Lock()
	defer s.runnersMu.Unlock()
	// Stop or a terminal latch may have landed while the attach's round
	// trip was in flight. Runners started now would have nobody left to
	// stop them, so a session that ended first never starts one.
	if !s.running.Load() || s.remoteEnded.Load() {
		return
	}
	s.heartbeat = NewHeartbeatRunner(s.client, remoteID, s.opts.HeartbeatPeriod)
	s.heartbeat.Start(ctx)
	if s.poller != nil {
		s.poller.SetSessionID(remoteID)
		s.poller.Start(ctx)
	}
}

// stopRunners stops the heartbeat and the poller, whichever of them exist.
// Safe from any goroutine; each runner's own Stop is idempotent.
func (s *SyncSession) stopRunners(ctx context.Context) {
	s.runnersMu.Lock()
	hb, poller := s.heartbeat, s.poller
	s.runnersMu.Unlock()
	if hb != nil {
		hb.Stop(ctx)
	}
	if poller != nil {
		poller.Stop(ctx)
	}
}

// statusRunner returns the heartbeat runner, or nil before the first event
// attaches.
func (s *SyncSession) statusRunner() *HeartbeatRunner {
	s.runnersMu.Lock()
	defer s.runnersMu.Unlock()
	return s.heartbeat
}
