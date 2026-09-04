package chatsync

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"
)

// workerLoop is the projection goroutine. It performs the one-time deferred
// attach (ensureAttached), projects bus events and appends them to the
// outbox, and it does NOTHING else: no other network call runs here.
//
// It used to also own the push. One select served the event channel, the
// per-event flush signal and the ticker, so under a streaming model the loop
// alternated one projection (~0.4ms, an fsync) with one FlushOutbox (one HTTP
// round trip, ~280ms measured) and throughput collapsed to one delta per
// round trip: appends landed at an exact RTT cadence, one 1-12 byte delta
// each, and the remote viewer fell further behind for as long as the model
// streamed (1.4s -> 6.6s over 7s, measured live). The push now runs on
// uploaderLoop, and the outbox is the durable handoff between the two.
func (s *SyncSession) workerLoop(ctx context.Context) {
	defer close(s.doneCh)

	for {
		// A terminal stop latched. There is nothing left to push, so exit
		// without a final flush rather than replay into a dead session. The
		// uploader still has to be released: it may be parked on its select.
		if s.remoteEnded.Load() {
			s.releaseUploader(ctx)
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
			// The first event attaches; if the attach failed, the terminal
			// stop above exits the loop on the next iteration and this
			// event is dropped - sync is dead, the local chat is not.
			if s.ensureAttached(ctx) == nil {
				s.processEvent(ctx, ev)
			}
		}
	}
}

// uploaderLoop is the push goroutine. It owns FlushOutbox, the retry
// schedule, the recovery and rebase paths and push health - everything that
// used to run on the worker after an append. It is woken by the per-event
// signal on flushCh (buffered 1, so a storm of signals coalesces into one
// wake) and by the ticker, and each wake drains the outbox to empty in
// batches of up to maxAppendBatch, so under a streaming storm one POST
// carries what arrived during the previous round trip rather than one delta.
//
// It returns on a terminal stop, or after the final flush the worker hands it
// on finalCh with the shutdown deadline. It never returns on ctx alone: the
// worker owns the shutdown order (drain, final append, then final flush),
// and an uploader that left first would strand the drained tail.
func (s *SyncSession) uploaderLoop(ctx context.Context) {
	defer close(s.uploaderDone)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if s.remoteEnded.Load() {
			return
		}
		// The final pass wins over a pending wake: it carries the shutdown
		// deadline and covers everything the wake would have pushed.
		select {
		case finalCtx := <-s.finalCh:
			s.setFinalUploadError(s.flushFinal(finalCtx))
			return
		default:
		}
		select {
		case finalCtx := <-s.finalCh:
			s.setFinalUploadError(s.flushFinal(finalCtx))
			return
		case <-s.flushCh:
			s.flush(ctx)
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

// releaseUploader wakes the uploader for its final pass and waits for it to
// finish. finalCh is buffered and sent on exactly once, so the send cannot
// block; the wait is what makes doneCh mean "nothing touches the outbox any
// more", which Stop's timed-out path relies on before closing it.
func (s *SyncSession) releaseUploader(ctx context.Context) error {
	select {
	case <-s.uploaderDone:
		return s.finalUploadError()
	default:
	}
	select {
	case s.finalCh <- ctx:
	default:
	}
	<-s.uploaderDone
	return s.finalUploadError()
}

func (s *SyncSession) setFinalUploadError(err error) {
	if err == nil {
		return
	}
	s.finalErr.Store(&err)
}

func (s *SyncSession) finalUploadError() error {
	if err := s.finalErr.Load(); err != nil {
		return *err
	}
	return nil
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
//
// It runs on the uploader goroutine. Every failure branch below mutates
// uploader-only state (the retry schedule, the recovery counters) or takes
// s.mu for a network-free projector mutation; none of it runs on the worker.
func (s *SyncSession) flushNow(ctx context.Context) {
	if s.remoteEnded.Load() {
		return
	}
	// Before the first event attaches there is nothing in the outbox and no
	// session to push to. The ticker still wakes this path every 100ms in
	// exactly that state - a session the user opened and has not used - so
	// this gate is what keeps an idle, never-messaged CLI silent.
	if !s.attached.Load() {
		return
	}
	sessionID := s.SessionID()
	batchID := sessionID + "-" + strconv.FormatUint(s.uploadBatch.Add(1), 10)
	unflushed, _ := s.outbox.UnflushedEvents()
	if len(unflushed) > 0 {
		s.telemetry.uploadStarted(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, unflushed[0].Seq, unflushed[len(unflushed)-1].Seq, len(unflushed))
	}

	moved, err := FlushOutboxWithTrace(ctx, s.client, s.outbox, sessionID, batchID, s.opts.ProjectorOptions.WriterID)
	if err == nil {
		s.telemetry.uploadFinished(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, s.LastSeq(), s.outbox.UnflushedCount(), moved)
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
	if len(unflushed) > 0 {
		s.telemetry.uploadFailed(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, unflushed[0].Seq, s.outbox.UnflushedCount(), err)
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

// flushFinal performs the single upload owned by shutdown. It does not enter
// the normal retry or recovery state machine: the uploader exits after this
// call, and the durable outbox must remain available for a later retry.
func (s *SyncSession) flushFinal(ctx context.Context) error {
	if s.remoteEnded.Load() {
		return nil
	}
	// A session that never got a message never attached, so it has nothing
	// to flush and no session to flush to; Stop must close it without a
	// single request.
	if !s.attached.Load() {
		return nil
	}
	sessionID := s.SessionID()
	batchID := sessionID + "-" + strconv.FormatUint(s.uploadBatch.Add(1), 10)
	unflushed, readErr := s.outbox.UnflushedEvents()
	if readErr != nil {
		return fmt.Errorf("final upload could not read outbox: %w", readErr)
	}
	if len(unflushed) == 0 {
		return nil
	}
	s.telemetry.uploadStarted(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, unflushed[0].Seq, unflushed[len(unflushed)-1].Seq, len(unflushed))
	moved, err := FlushOutboxWithTrace(ctx, s.client, s.outbox, sessionID, batchID, s.opts.ProjectorOptions.WriterID)
	if err != nil {
		s.telemetry.uploadFailed(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, unflushed[0].Seq, s.outbox.UnflushedCount(), err)
		return s.withUnsentRange(err)
	}
	s.telemetry.uploadFinished(s.localSessionID, s.opts.ProjectorOptions.WriterID, batchID, s.LastSeq(), s.outbox.UnflushedCount(), moved)
	s.retryBase = 0
	s.retryAt = time.Time{}
	s.lastGapBase = noGapBase
	s.reportHealth(s.health.noteSuccess(s.outbox.UnflushedCount()), "")
	return nil
}

func (s *SyncSession) withUnsentRange(cause error) error {
	unflushed, err := s.outbox.UnflushedEvents()
	if err != nil {
		return fmt.Errorf("final upload failed; unsent sequence range unavailable: %w: %v", cause, err)
	}
	if len(unflushed) == 0 {
		return fmt.Errorf("final upload failed; unsent sequence range empty: %w", cause)
	}
	return fmt.Errorf("final upload failed; unsent sequence range %d-%d: %w", unflushed[0].Seq, unflushed[len(unflushed)-1].Seq, cause)
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
// and the heartbeat down. It is reached through classifyFlushError's
// outcomeStop, the recovery bounds, a dead outbox, or a failed deferred attach
// (see ensureAttached); a 409 or 404 on its own recovers instead
// (recoverRemoteSession). The local chat is untouched.
//
// It is called from the uploader or the worker goroutine, so the (blocking)
// runner stops are detached. Both runner Stop methods are idempotent, so a
// later Stop(ctx) that races this one is safe.
func (s *SyncSession) handleRemoteEnd(ctx context.Context, reason string) {
	if !s.remoteEnded.CompareAndSwap(false, true) {
		return
	}
	s.stopReason.Store(&reason)
	s.health.noteStop(reason, s.outbox.UnflushedCount())
	go func() {
		s.stopRunners(ctx)
		// The "say so" half of the contract's poison rule. The
		// CompareAndSwap above makes this exactly-once, and running it here
		// - already off the uploader - keeps a host callback that blocks on
		// a terminal or a full UI channel away from the final flush.
		if s.opts.OnStop != nil {
			s.opts.OnStop(reason)
		}
	}()
}

// reportHealth fires the host callback for a health transition, detached
// from the uploader for the same reason OnStop is: a host that blocks on a
// terminal or a full UI channel must not hold up the next push.
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

// drainAndFlushFinal is the worker's shutdown: project everything still
// queued, append the final drop marker, then hand the uploader the shutdown
// deadline for its final flush and wait for it. The order is load-bearing -
// the final flush must see the drained tail in the outbox, so the append
// happens before the release.
func (s *SyncSession) drainAndFlushFinal(ctx context.Context) {
	for {
		select {
		case ev := <-s.eventCh:
			// Events queued while shutdown began are real content: the
			// deferred attach runs for them here, so the final flush below
			// can deliver the tail. A session with NO queued events never
			// attaches - closing an unused session stays silent.
			if s.ensureAttached(ctx) == nil {
				s.processEvent(ctx, ev)
			}
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

	_ = s.releaseUploader(ctx)
}
