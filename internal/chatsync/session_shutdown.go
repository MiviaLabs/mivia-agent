package chatsync

import (
	"context"
	"fmt"
)

// Stop terminates the session sync loop, flushes pending events, and closes
// resources. Every wait it performs is bounded by ctx: a caller that gives Stop
// a 200ms deadline gets control back inside 200ms even when a long poll is
// parked and the final append is stalled on a dead network.
//
// When the deadline arrives first, Stop returns ctx.Err() and hands the outbox
// close to a goroutine that waits for the worker, because closing the outbox
// under a worker still writing to it would race its file handle.
func (s *SyncSession) Stop(ctx context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return nil
	}

	// The worker's final drain and flush inherits the caller's deadline.
	select {
	case s.stopCtxCh <- ctx:
	default:
	}
	close(s.stopCh)

	timedOut := false
	select {
	case <-s.doneCh:
	case <-ctx.Done():
		timedOut = true
	}

	// Unsubscribe only AFTER the worker read Drops() for the last time.
	// events.Subscription documents that reading the counter after
	// Unsubscribe can over-report: Publish snapshots the subscriber slice
	// under the lock and enqueues outside it, so a publish already in flight
	// still lands in the removed subscription's queue and is dropped there.
	// Nothing was lost - no handler was ever going to run for it - but the
	// counter moves. Reading it in that window makes the final flush record a
	// sync.dropped marker for a hole that does not exist, and settled decision
	// 6 makes that marker PERMANENT in the transcript.
	//
	// On the TIMEOUT path the worker is still running, so releasing here would
	// reopen exactly that window: the worker's drainAndFlushFinal has not yet
	// made its last Drops() read. The release therefore moves into the same
	// goroutine that already waits for the worker before closing the outbox.
	if !timedOut && s.unsubscribe != nil {
		s.unsubscribe()
	}

	if s.heartbeat != nil {
		s.heartbeat.Stop(ctx)
	}
	if s.poller != nil {
		s.poller.Stop(ctx)
	}

	if timedOut {
		go func() {
			<-s.doneCh
			if s.unsubscribe != nil {
				s.unsubscribe()
			}
			// The worker is done, so this cannot race its own records. A
			// drain that overran the deadline is the stuck-server case the
			// file exists to diagnose, and it must not be left reading
			// "healthy".
			s.health.noteStop("session closed, final drain timed out", s.outbox.UnflushedCount())
			_ = s.outbox.Close()
		}()
		return ctx.Err()
	}

	// This is a SECOND, independent flush, off the worker. In the ordinary
	// case drainAndFlushFinal has already emptied the outbox and it is a
	// no-op. Its failure is CLASSIFIED and recorded, never recovered: the
	// worker is gone, so recovery's lock discipline has no owner. The
	// remoteEnded guard means it fires only on non-latching outcomes - an
	// interval or throttle defer, a transient failure - never after a poison
	// or a no-progress stop, so a reader must not expect it there.
	reason := "session closed"
	if s.remoteEnded.Load() {
		reason = s.StopReason()
	} else if _, err := FlushOutbox(ctx, s.client, s.outbox, s.SessionID()); err != nil {
		reason = fmt.Sprintf("session closed, final push failed (%s): %v", classifyFlushError(err), err)
	}
	// A terminal latch already recorded its own reason; noteStop keeps the
	// first one. This call is what makes the record outlive the process.
	s.health.noteStop(reason, s.outbox.UnflushedCount())
	return s.outbox.Close()
}

// shutdownCtx returns the deadline Stop asked the worker to shut down under,
// falling back to the session context when the worker is unwinding for another
// reason (a cancelled session context).
func (s *SyncSession) shutdownCtx(base context.Context) context.Context {
	select {
	case c := <-s.stopCtxCh:
		if c != nil {
			return c
		}
	default:
	}
	return base
}
