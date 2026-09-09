package chatsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The worker's unwinding paths that a live session never reaches in the
// suite: the terminal-stop exit, and the shutdown deadline it unwinds under.

// TestTheWorkerReleasesTheUploaderOnATerminalStop pins the exit at the top of
// workerLoop.
//
// A latched terminal stop (a fatal auth failure, a poison event, a
// no-progress stop) means there is nothing left to push, so the worker exits
// without a final flush rather than replaying into a dead session. But it
// cannot simply return: the uploader may be parked on its select, and doneCh
// is the signal every caller reads as "nothing touches the outbox any more" -
// Stop's timed-out path closes the outbox on the strength of it. A worker
// that returned without releasing the uploader would close the outbox under a
// goroutine still holding it.
func TestTheWorkerReleasesTheUploaderOnATerminalStop(t *testing.T) {
	s := &SyncSession{
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		eventCh:      make(chan events.Event, 1),
		finalCh:      make(chan context.Context, 1),
		uploaderDone: make(chan struct{}),
		stopCtxCh:    make(chan context.Context, 1),
	}
	s.remoteEnded.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.workerLoop(ctx)

	// The uploader is still running, so the worker must be waiting on it.
	select {
	case <-s.doneCh:
		t.Fatal("the worker closed doneCh while the uploader was still running; Stop's timed-out path " +
			"closes the outbox on doneCh alone and would pull it out from under the uploader")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case got := <-s.finalCh:
		if got != ctx {
			t.Errorf("the worker handed the uploader %v, want the context it is unwinding under", got)
		}
	default:
		t.Fatal("the worker never woke the uploader for its final pass; an uploader parked on its select " +
			"would then never return and doneCh would never close")
	}

	close(s.uploaderDone)
	select {
	case <-s.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not return once the uploader was done")
	}
}

// TestTheShutdownContextFallsBackToTheSessionContext pins shutdownCtx's
// fallback. The worker also unwinds for reasons Stop did not ask for - a
// cancelled session context, a terminal latch - and there is then no deadline
// waiting on stopCtxCh. Returning what the channel yielded regardless would
// hand drainAndFlushFinal a nil context, and every call it makes with it
// panics on the session's own worker goroutine.
func TestTheShutdownContextFallsBackToTheSessionContext(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("nothing queued", func(t *testing.T) {
		s := &SyncSession{stopCtxCh: make(chan context.Context, 1)}
		if got := s.shutdownCtx(base); got != base {
			t.Errorf("shutdownCtx = %v, want the session context %v", got, base)
		}
	})

	t.Run("a nil context queued", func(t *testing.T) {
		s := &SyncSession{stopCtxCh: make(chan context.Context, 1)}
		s.stopCtxCh <- nil
		got := s.shutdownCtx(base)
		if got == nil {
			t.Fatal("shutdownCtx returned nil; the final drain would panic on it")
		}
		if got != base {
			t.Errorf("shutdownCtx = %v, want the session context %v", got, base)
		}
	})

	t.Run("Stop's deadline queued", func(t *testing.T) {
		s := &SyncSession{stopCtxCh: make(chan context.Context, 1)}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Minute)
		defer stopCancel()
		s.stopCtxCh <- stopCtx
		if got := s.shutdownCtx(base); got != stopCtx {
			t.Errorf("shutdownCtx = %v, want Stop's deadline; the final drain must honour the caller's "+
				"bound, not the session's", got)
		}
	})
}

// TestASessionWithNoDropSourceReportsNoLoss pins currentDrops' guard at its
// real call site.
//
// The drop source is installed near the END of OpenSession, after the outbox
// is open and after the fork rebase, and it is torn down with the
// subscription. Every projection reads it. Without the guard a projection in
// either window dereferences a nil func on the worker goroutine and takes the
// host process down; with it, the session reports the honest "no loss known"
// answer and the transcript is merely missing a marker it has no source for.
func TestASessionWithNoDropSourceReportsNoLoss(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-nodrop")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-nodrop", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "No Drop Source",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	syncSess.swapDropSource(nil)

	if got := syncSess.currentDrops(); got != 0 {
		t.Errorf("currentDrops() = %d with no source installed, want 0", got)
	}

	publishTurnStart(bus, "sess-nodrop", "turn:1", "projected with no drop source")
	waitForSeq(t, syncSess, 1)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop: %v", err)
	}

	items := rec.items()
	if len(items) == 0 {
		t.Fatal("nothing reached the wire; the projection did not survive the missing drop source")
	}
	for _, item := range items {
		if item.Type == TypeSyncDropped {
			t.Errorf("a sync.dropped marker reached the wire with no drop source installed; the session "+
				"has no evidence of any loss and must not invent a hole: %+v", item)
		}
	}
}
