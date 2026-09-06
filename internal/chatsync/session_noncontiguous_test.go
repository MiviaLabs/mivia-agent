package chatsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

const nonContiguousMsg = "Non-contiguous batch: events[1] has seq 4, expected 3"

// openAgainstFakeWithTelemetry is openAgainstFake with a telemetry handle, so a
// test can observe the one flush a batch-less push acknowledges.
func openAgainstFakeWithTelemetry(t *testing.T, f *fakeAPI, localID, outboxDir string, tel *SyncTelemetry) (*events.Bus, *SyncSession) {
	t.Helper()
	bus := events.New()
	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: f.URL()},
		RemoteSessionID: localID,
		OutboxDir:       outboxDir,
		MaxUnflushed:    100,
		CreateTitle:     "Non-contiguous",
		HeartbeatPeriod: 10 * time.Minute,
		Telemetry:       tel,
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

// assertOneSessionNoFork pins the "heal on the ORIGINAL session" half of the
// non-contiguous contract: a rebase, not a recovery. A fork marker in any
// batch, a second session id, or a terminal stop is the wrong mechanism.
func assertOneSessionNoFork(t *testing.T, f *fakeAPI, s *SyncSession) {
	t.Helper()
	if s.Stopped() {
		t.Errorf("sync stopped (%q); a non-contiguous batch heals by rebase, it does not stop", s.StopReason())
	}
	if n := len(f.SessionIDs()); n != 1 {
		t.Errorf("%d sessions created, want 1: the backlog forked instead of rebasing", n)
	}
	for _, b := range f.Batches() {
		for _, ev := range b {
			if ev.Type == TypeSyncForked {
				t.Error("a fork marker was published; a non-contiguous batch must rebase, not fork")
			}
		}
	}
}

// seedOutboxSeqs writes the given seqs as turn-started events into a closed
// outbox at dir, with the cursor left at 0 so every event is unflushed.
func seedOutboxSeqs(t *testing.T, dir string, seqs ...int64) {
	t.Helper()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	for _, seq := range seqs {
		if err := ob.Append(WireEvent{
			Seq:     seq,
			Type:    TypeTurnStarted,
			Payload: &TurnStartedPayload{Envelope: Envelope{V: 1, Turn: "turn:seed"}, Text: "e"},
		}); err != nil {
			t.Fatalf("seed Append seq %d: %v", seq, err)
		}
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}

// TestFlush_NonContiguous400RebasesAndContinues covers the second recoverable
// 400 shape: the server names a NON-CONTIGUOUS batch, the in-batch gap the
// projector's closeTurn inversion used to produce. The gap is inside the
// outbox's unflushed tail, so no resend can fix it; the settled rebase on the
// server's mark renumbers or drops the tail and the stream continues on the
// original session. Treating it as poison stops a healable stream.
func TestFlush_NonContiguous400RebasesAndContinues(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("noncontig-rebase")
	// The server mark sits at the outbox high-water seq, so the attach's
	// openingSeq leaves the seeded tail alone instead of renumbering the gap
	// away before the first push.
	f.AdvanceServerSeq(id, 4)

	dir := t.TempDir()
	seedOutboxSeqs(t, dir, 1, 2, 4)
	// Seqs 1 and 2 are delivered; the gap at 3 is inside the unflushed tail.
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	if err := ob.AdvanceCursor(2); err != nil {
		t.Fatalf("seed AdvanceCursor: %v", err)
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	f.RejectAppendsWith(400, "Bad Request", nonContiguousMsg)
	bus, s := openAgainstFake(t, f, id, dir)

	publishTurnStart(bus, id, "turn:trigger", "trigger the push")
	waitUntil(t, "the first push attempt", func() bool { return len(f.Batches()) >= 1 })
	f.ClearAppendRejection()

	waitUntil(t, "the rebased tail to be accepted", func() bool { return f.LastSeq(id) == 5 })
	assertOneSessionNoFork(t, f, s)
	if next := s.LastSeq(); next != 5 {
		t.Errorf("projector LastSeq() = %d, want 5; the next event must continue the rebased stream", next)
	}
}

// TestNonContiguous400AtNewMarkRebasesAgain pins lastGapBase reset-on-success:
// a successful push clears the recorded rebase mark, so a LATER non-contiguous
// 400 at the same mark gets a fresh rebase instead of the loop guard's fork.
// The first rebase here empties the outbox, so the success in between is the
// batch-less push, observable only through telemetry's success timestamp.
func TestNonContiguous400AtNewMarkRebasesAgain(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("noncontig-again")
	// The mark is PAST the seeded tail, so the first rebase (mark 6) drops
	// every seeded event and empties the outbox.
	f.AdvanceServerSeq(id, 6)

	dir := t.TempDir()
	seedOutboxSeqs(t, dir, 1, 2)

	tel := NewSyncTelemetry(nil)
	f.RejectAppendsWith(400, "Bad Request", nonContiguousMsg)
	bus, s := openAgainstFakeWithTelemetry(t, f, id, dir, tel)

	// First complaint: the batch 1,2,6 is non-contiguous; the rebase on mark 6
	// drops the acknowledged prefix and empties the outbox.
	publishTurnStart(bus, id, "turn:1", "first message")
	waitUntil(t, "the first push attempt", func() bool { return len(f.Batches()) >= 1 })

	// The success in between: the empty-outbox push, which the server
	// acknowledges without advancing its mark.
	f.ClearAppendRejection()
	waitUntil(t, "the batch-less push to be acknowledged", func() bool {
		return !tel.Snapshot().LastSuccessAt.IsZero()
	})

	// Second complaint at the SAME mark. A stale recorded base would read as
	// "the rebase moved nothing" and fork; the reset-on-success must rebase.
	f.RejectAppendsWith(400, "Bad Request", strings.Replace(nonContiguousMsg, "seq 4", "seq 7", 1))
	publishTurnStart(bus, id, "turn:2", "second message")
	waitUntil(t, "the second push attempt", func() bool { return len(f.Batches()) >= 2 })
	f.ClearAppendRejection()

	waitUntil(t, "the tail after the second rebase to be accepted", func() bool { return f.LastSeq(id) == 7 })
	assertOneSessionNoFork(t, f, s)
}
