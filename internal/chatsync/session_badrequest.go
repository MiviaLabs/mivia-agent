package chatsync

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// noGapBase means no gap rebase has been attempted since the last successful
// push. It is negative so that a legitimate server mark of 0 - the state of a
// session that has never been appended to - is still allowed one rebase.
const noGapBase = int64(-1)

// handleBadRequest implements the settled 400 policy.
//
// A sequence-gap 400 re-reads GET /:id and rebases on serverLastSeq+1
// (chat-sync-cli-slice.md:161-164). Treating it as fatal "guarantees the
// failure it is trying to avoid": the most common cause is the crash window
// between the server's commit and the local cursor fsync, which settled
// decision 4 knowingly accepts.
//
// An oversize 400 and every other 400 are poison. The body is already durable
// in the outbox and byte-identical on every replay, so a retry resubmits a
// request the server has already judged malformed, on the flush ticker, for as
// long as the process lives. Sync stops and says why
// (chat-sync-event-contract.md:285-287).
//
// It runs on the worker goroutine, like the rest of the retry schedule.
func (s *SyncSession) handleBadRequest(ctx context.Context, err error) {
	var bad *BadRequestError
	if !errors.As(err, &bad) || !bad.IsSequenceComplaint() {
		s.poison(ctx, err)
		return
	}

	sess, getErr := s.client.GetSession(ctx, s.SessionID())
	if getErr != nil {
		// The server's mark is the only evidence that could classify this
		// batch, and it is unreadable. Stopping here would convert a network
		// blip into permanent data loss, so treat it as transient.
		s.scheduleRetry()
		return
	}

	// A second gap at the same server mark means the previous rebase moved
	// nothing. Repeating it is an unbounded retry against a body the server
	// will never accept.
	if s.lastGapBase == sess.LastSeq {
		s.poison(ctx, fmt.Errorf("rebasing on serverLastSeq=%d did not close the gap: %w", sess.LastSeq, err))
		return
	}

	if rebaseErr := s.rebaseOn(sess.LastSeq); rebaseErr != nil {
		s.poison(ctx, fmt.Errorf("rebase on serverLastSeq=%d failed: %w; original rejection: %v",
			sess.LastSeq, rebaseErr, err))
		return
	}

	s.lastGapBase = sess.LastSeq
	s.retryBase = 0
	s.retryAt = time.Time{}
}

// rebaseOn realigns the outbox and the seq counter onto the server's mark.
//
// Two shapes, and the difference matters because one of them must NOT renumber:
//
//   - The server holds a prefix of what the outbox still holds, or all of it
//     (serverLastSeq >= firstUnflushed-1). Dropping the acknowledged prefix
//     leaves the remainder already contiguous from serverLastSeq+1, and no
//     event body changes - which keeps replay byte-identical, the property the
//     API's idempotency rests on.
//   - The server is BEHIND the outbox's first unflushed seq. The events in
//     between are gone and no resend can produce them, so the events are
//     renumbered onto serverLastSeq+1. The loss itself is what the sync.dropped
//     marker path records; this only restores contiguity.
func (s *SyncSession) rebaseOn(serverLastSeq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	unflushed, err := s.outbox.UnflushedEvents()
	if err != nil {
		return fmt.Errorf("read unflushed events: %w", err)
	}

	if len(unflushed) == 0 || serverLastSeq >= unflushed[0].Seq-1 {
		if err := s.outbox.AdvanceCursor(serverLastSeq); err != nil {
			return fmt.Errorf("advance cursor to server mark: %w", err)
		}
		if serverLastSeq > s.projector.LastSeq() {
			s.projector.ResetSeq(serverLastSeq)
		}
		return nil
	}

	count, err := s.outbox.Rebase(serverLastSeq)
	if err != nil {
		return fmt.Errorf("rebase outbox: %w", err)
	}
	s.projector.ResetSeq(serverLastSeq + int64(count))
	return nil
}

// poison stops sync terminally and records why. It reuses the remote-end path
// rather than inventing a second stop mechanism: the effect is identical -
// pusher, poller and heartbeat down, local chat untouched.
func (s *SyncSession) poison(ctx context.Context, err error) {
	s.handleRemoteEnd(ctx, fmt.Sprintf("sync stopped, the server rejected the batch and no retry can fix it: %v", err))
}

// scheduleRetry arms the jittered push-retry schedule for the next attempt.
func (s *SyncSession) scheduleRetry() {
	s.retryBase = nextRetryBackoff(s.retryBase)
	s.retryAt = time.Now().Add(jitterBackoff(s.retryBase))
}
