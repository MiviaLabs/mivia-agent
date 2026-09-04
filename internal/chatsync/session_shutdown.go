package chatsync

import (
	"context"
)

// Stop terminates the session sync loop, flushes pending events, and closes
// resources. Every wait it performs is bounded by ctx: a caller that gives Stop
// a 200ms deadline gets control back inside 200ms even when a long poll is
// parked and the final append is stalled on a dead network.
//
// When the deadline arrives first, Stop returns ctx.Err() and hands the outbox
// close to a goroutine that waits for the worker - which itself waits for the
// uploader - because closing the outbox under a goroutine still using it
// would race its file handle.
func (s *SyncSession) Stop(ctx context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return nil
	}
	// Cancel the context used by the normal uploader before waiting for the
	// worker. This interrupts an active HTTP request; the final upload below
	// uses the caller's still-bounded Stop context.
	if s.uploaderCancel != nil {
		s.uploaderCancel()
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

	s.stopRunners(ctx)

	if timedOut {
		go s.finishTimedOutStop()
		return ctx.Err()
	}

	return s.finishStop()
}

func (s *SyncSession) finishTimedOutStop() {
	defer close(s.shutdownDone)
	<-s.doneCh
	if s.unsubscribe != nil {
		s.unsubscribe()
	}
	reason := "session closed, final drain timed out"
	if err := s.finalUploadError(); err != nil {
		reason += ": " + err.Error()
	}
	s.health.noteStop(reason, s.outbox.UnflushedCount())
	_ = s.outbox.Close()
}

func (s *SyncSession) finishStop() error {
	defer close(s.shutdownDone)
	reason := "session closed"
	if s.remoteEnded.Load() {
		reason = s.StopReason()
	} else if err := s.finalUploadError(); err != nil {
		reason = err.Error()
	}
	// A terminal latch already recorded its own reason; noteStop keeps the
	// first one. This call is what makes the record outlive the process.
	s.health.noteStop(reason, s.outbox.UnflushedCount())
	closeErr := s.outbox.Close()
	if err := s.finalUploadError(); err != nil {
		return err
	}
	return closeErr
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
