package chatsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after 3s waiting for %s", what)
}

func openAgainstFake(t *testing.T, f *fakeAPI, localID, outboxDir string) (*events.Bus, *SyncSession) {
	t.Helper()
	bus := events.New()
	opts := SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: f.URL()},
		// localID plays both roles here: the fake's remote session id and the
		// id the test stamps on the events it publishes. Production keeps them
		// apart - the chat principal filters the bus, the persisted remote id
		// names the URL - and OpenSession now takes them separately.
		RemoteSessionID: localID,
		OutboxDir:       outboxDir,
		MaxUnflushed:    100,
		CreateTitle:     "Bad Request",
		HeartbeatPeriod: 10 * time.Minute,
	}
	s, err := OpenSession(context.Background(), bus, localID, opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)
	})
	return bus, s
}

// TestFlush_SequenceGap400RebasesAndContinues covers the settled recovery
// (chat-sync-cli-slice.md:194-197): a stream that reaches the server out of
// order is renumbered onto the server's mark and continues contiguously.
// Treating it as fatal "guarantees the failure it is trying to avoid".
//
// The state is the crash window settled decision 4 explicitly accepts: the
// cursor was fsynced past events the server never received, so the outbox holds
// seqs 5..7 while the session is still at 0. Under the deferred attach that
// renumbering happens when the first message attaches (openingSeq), before any
// push - so the trigger event below is both the attach and the continuation,
// and the whole stream must land contiguous from 1.
func TestFlush_SequenceGap400RebasesAndContinues(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("gap-rebase")
	dir := t.TempDir()

	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	for seq := int64(1); seq <= 7; seq++ {
		// Close "turn:1" on the last seeded event so this fixture models a
		// clean crash window (unsent but fully closed turns), not a
		// dangling open turn - reconcileDangling runs on attach and would
		// otherwise synthesize an extra turn.failed event that this test
		// is not about.
		if seq == 7 {
			if err := ob.Append(WireEvent{
				Seq:     seq,
				Type:    TypeTurnEnded,
				Payload: &TurnEndedPayload{Envelope: Envelope{V: 1, Turn: "turn:1"}, Reason: "done"},
			}); err != nil {
				t.Fatalf("seed Append: %v", err)
			}
			continue
		}
		if err := ob.Append(WireEvent{
			Seq:     seq,
			Type:    TypeTurnStarted,
			Payload: &TurnStartedPayload{Envelope: Envelope{V: 1, Turn: "turn:1"}, Text: "e"},
		}); err != nil {
			t.Fatalf("seed Append: %v", err)
		}
	}
	if err := ob.AdvanceCursor(4); err != nil {
		t.Fatalf("seed AdvanceCursor: %v", err)
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	bus, s := openAgainstFake(t, f, id, dir)

	publishTurnStart(bus, id, "turn:trigger", "first message")
	waitUntil(t, "the rebased batch to be accepted", func() bool {
		return f.LastSeq(id) == 4
	})
	if got := len(f.Events(id)); got != 4 {
		t.Errorf("the server stored %d events, want 4 (5..7 rebased onto 1..3 by the attach, then the trigger event as 4)", got)
	}
	if s.Stopped() {
		t.Errorf("sync stopped on a sequence-gap 400; the settled behaviour is rebase and continue (reason: %q)", s.StopReason())
	}
	if next := s.LastSeq(); next != 4 {
		t.Errorf("projector LastSeq() = %d, want 4; the next event must continue the rebased stream", next)
	}
}

// TestFlush_Oversize400StopsSync covers the poison half
// (chat-sync-event-contract.md:285-287): a 400 no retry can fix must stop
// syncing and SAY SO. Without it the flush resubmits the identical oversize
// body on every tick, for ever.
func TestFlush_Oversize400StopsSync(t *testing.T) {
	f := newFakeAPI(t)
	f.SetMaxPayloadBytes(200)
	id := f.NewSession("oversize-poison")

	bus, s := openAgainstFake(t, f, id, t.TempDir())

	publishTurnStart(bus, id, "turn:1", strings.Repeat("x", 600))

	waitUntil(t, "sync to stop on the oversize 400", func() bool { return s.Stopped() })
	if reason := s.StopReason(); reason == "" {
		t.Error("sync stopped with no reason; a poison stop that says nothing is indistinguishable from a healthy idle session")
	} else if !strings.Contains(reason, "400") {
		t.Errorf("stop reason = %q; it must name the 400 that poisoned the stream", reason)
	}

	before := len(f.Batches())
	time.Sleep(400 * time.Millisecond)
	if after := len(f.Batches()); after != before {
		t.Errorf("the flusher sent %d more batches after the poison stop; a 400 a retry cannot fix must not be retried", after-before)
	}
	if got := f.LastSeq(id); got != 0 {
		t.Errorf("server lastSeq = %d, want 0; nothing in the poisoned batch may be applied", got)
	}
}

// TestFlush_UnclassifiedOther400StopsSync covers "400, other: poison. A client
// bug a retry cannot fix" (chat-sync-cli-slice.md:196). A 400 that does not
// name a sequence problem must never be rebased.
func TestFlush_UnclassifiedOther400StopsSync(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("other-poison")
	f.RejectAppendsWith(400, "Bad Request", "type must be at most 100 characters")

	bus, s := openAgainstFake(t, f, id, t.TempDir())
	publishTurnStart(bus, id, "turn:1", "hello")

	waitUntil(t, "sync to stop on the unclassified 400", func() bool { return s.Stopped() })
	if reason := s.StopReason(); !strings.Contains(reason, "400") {
		t.Errorf("stop reason = %q; it must name the 400", reason)
	}
}

// TestFlush_SequenceGap400ThatRebaseCannotFixRecoversOntoANewSession is the
// loop guard, inverted. If the server keeps reporting the same base after a
// rebase, the rebase is not making progress and repeating it is an infinite
// retry against a body the server will never accept - but the body is not
// malformed, the SESSION is unusable, so the backlog moves to a fresh one
// exactly once and batches resume against the new id.
func TestFlush_SequenceGap400ThatRebaseCannotFixRecoversOntoANewSession(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("gap-no-progress")
	f.RejectAppendsWith(400, "Bad Request", "sequence gap: expected 1, got 9")

	bus, s := openAgainstFake(t, f, id, t.TempDir())
	publishTurnStart(bus, id, "turn:1", "hello")

	waitUntil(t, "recovery after a rebase that made no progress", func() bool {
		return len(f.SessionIDs()) == 2
	})
	if s.Stopped() {
		t.Fatalf("sync stopped (%q); a gap the rebase cannot close recovers, it does not spin or stop", s.StopReason())
	}
	newID := f.SessionIDs()[1]
	waitUntil(t, "a batch carrying the fork marker for the new session", func() bool {
		for _, b := range f.Batches() {
			for _, ev := range b {
				if ev.Type == TypeSyncForked {
					return true
				}
			}
		}
		return false
	})
	if got := s.SessionID(); got != newID {
		t.Errorf("SessionID() = %q, want %q", got, newID)
	}
	// The fake still rejects every append, so a second recovery would be
	// forking on the ticker; the interval refusal defers it instead.
	time.Sleep(400 * time.Millisecond)
	if n := len(f.SessionIDs()); n != 2 {
		t.Errorf("%d sessions created, want 2: recovery fired more than once inside the interval", n)
	}
}

// TestFlush_SequenceGap400WithUnreadableSessionRetries proves an unclassifiable
// failure is transient, not poison. If GET /:id itself fails there is no
// evidence the batch is bad, and stopping sync on a network blip would discard
// a recoverable session.
func TestFlush_SequenceGap400WithUnreadableSessionRetries(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("gap-unreadable")
	f.RejectAppendsWith(400, "Bad Request", "sequence gap: expected 1, got 9")

	bus, s := openAgainstFake(t, f, id, t.TempDir())
	// The deferred attach must complete BEFORE the session becomes unreadable:
	// an attach that cannot read the session is a startup failure, not the
	// flush-path case under test. One event triggers the attach; the batch it
	// pushes is rejected by the blanket rule above, which is the trigger the
	// rebase path needs.
	publishTurnStart(bus, id, "turn:attach", "first message")
	waitUntil(t, "the first push attempt (the attach ran)", func() bool { return len(f.Batches()) >= 1 })

	f.FailSessionReads(true)
	publishTurnStart(bus, id, "turn:1", "hello")

	time.Sleep(600 * time.Millisecond)
	if s.Stopped() {
		t.Errorf("sync stopped although the server state could not be read (reason %q); an unreadable session is transient, not poison", s.StopReason())
	}
}

// TestRebaseOn_AdvanceCursorBranchResetsProjectorSeq pins the ResetSeq
// call inside rebaseOn's advance-cursor shape (server at or ahead of the
// outbox's first unflushed seq): a fresh serverLastSeq strictly greater
// than the projector's current LastSeq must move the projector forward,
// on the ordinary success path (no fault injection needed).
func TestRebaseOn_AdvanceCursorBranchResetsProjectorSeq(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("rebase-advance")
	_, s := openAgainstFake(t, f, id, t.TempDir())

	before := s.projector.LastSeq()
	if err := s.rebaseOn(before + 5); err != nil {
		t.Fatalf("rebaseOn: %v", err)
	}
	if got := s.projector.LastSeq(); got != before+5 {
		t.Fatalf("projector.LastSeq() = %d, want %d after rebaseOn advanced past it", got, before+5)
	}
}

// TestRebaseOn_BehindShapeRebaseErrorSurfaces pins rebaseOn's own
// "server BEHIND the outbox" shape's Rebase-error wrap: this is the
// FIRST outbox write rebaseOn attempts on this shape (no earlier
// AdvanceCursor call to race against), so a read-only outbox directory
// isolates the failure cleanly.
func TestRebaseOn_BehindShapeRebaseErrorSurfaces(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("rebase-behind")
	f.RejectAppendsWith(400, "Bad Request", "sequence gap: expected 1, got 9")
	bus, s := openAgainstFake(t, f, id, t.TempDir())
	publishTurnStart(bus, id, "turn:seed", "seed an unflushed event")
	waitUntil(t, "the seed event is unflushed", func() bool {
		return s.outbox.UnflushedCount() > 0
	})

	dir := s.outbox.dir
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "writability-probe")
	if wf, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = wf.Close()
		_ = os.Remove(probe)
		t.Skip("platform still creates files in a read-only directory")
	}

	unflushed, err := s.outbox.UnflushedEvents()
	if err != nil {
		t.Fatal(err)
	}
	// Behind the outbox's first unflushed seq - 1, which is what selects
	// the "server BEHIND" shape (rebaseOn's own else-branch) rather than
	// the advance-cursor branch above.
	err = s.rebaseOn(unflushed[0].Seq - 2)
	if err == nil {
		t.Fatal("rebaseOn hid a Rebase failure on a read-only outbox directory")
	}
}

// TestFlush_RebaseFailureIsPoison pins session_badrequest.go's own poison
// branch: when rebaseOn itself fails (as opposed to failing to close the
// gap, which forks onto a new session instead), sync must stop
// terminally rather than retry a batch the outbox can no longer produce
// correctly.
//
// Driven by calling handleBadRequest directly rather than through the
// async flush ticker: the outbox directory needs to go read-only AFTER
// the session attaches (which itself writes to the outbox) but BEFORE
// rebaseOn's own write, and no reliable window for that exists once the
// uploader goroutine is live and racing the test.
func TestFlush_RebaseFailureIsPoison(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("rebase-poison")

	bus, s := openAgainstFake(t, f, id, t.TempDir())
	publishTurnStart(bus, id, "turn:attach", "first message")
	waitUntil(t, "the attach pushes its first batch", func() bool { return len(f.Batches()) >= 1 })
	waitUntil(t, "the outbox settles with nothing unflushed", func() bool { return s.outbox.UnflushedCount() == 0 })

	dir := s.outbox.dir
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "writability-probe")
	if wf, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = wf.Close()
		_ = os.Remove(probe)
		t.Skip("platform still creates files in a read-only directory")
	}

	// Empty outbox selects rebaseOn's advance-cursor shape unconditionally
	// (len(unflushed) == 0), whose first write - AdvanceCursor - now fails
	// on the read-only directory.
	s.handleBadRequest(context.Background(), &BadRequestError{
		StatusCode: 400, Message: "sequence gap: expected 1, got 9",
	})

	if !s.Stopped() {
		t.Fatal("handleBadRequest did not poison sync after rebaseOn itself failed")
	}
	if reason := s.StopReason(); !strings.Contains(reason, "rebase") {
		t.Errorf("stop reason = %q, want it to name the rebase failure", reason)
	}
}
