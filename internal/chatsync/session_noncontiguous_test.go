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

	// A gap anywhere in seqs makes repairEventsFile drop everything past
	// the last contiguous prefix, on the very next open (every caller
	// reopens this outbox for AdvanceCursor before attach). Close
	// "turn:seed" at the end of THAT surviving prefix, not at the end of
	// the whole slice, so what actually lands on disk is never a
	// dangling open turn - reconcileDangling runs on attach and would
	// otherwise synthesize an extra turn.failed event these rebase tests
	// are not about.
	closeAt := len(seqs) - 1
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			closeAt = i - 1
			break
		}
	}

	for i, seq := range seqs {
		ev := WireEvent{
			Seq:     seq,
			Type:    TypeTurnStarted,
			Payload: &TurnStartedPayload{Envelope: Envelope{V: 1, Turn: "turn:seed"}, Text: "e"},
		}
		if i == closeAt {
			ev = WireEvent{
				Seq:     seq,
				Type:    TypeTurnEnded,
				Payload: &TurnEndedPayload{Envelope: Envelope{V: 1, Turn: "turn:seed"}, Reason: "done"},
			}
		}
		if err := ob.Append(ev); err != nil {
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

// TestFlush_NonContiguousTailWithMarkAtTailStartRebasesTheTail covers the
// in-tail gap at mark == firstUnflushed-1: dropping the acknowledged prefix
// alone heals nothing there, because the remainder itself is not contiguous.
// The rebase must fall through to a full renumber of the remainder onto the
// mark; a cursor-only advance retries the same rejected batch and wedges the
// stream into the fork path.
func TestFlush_NonContiguousTailWithMarkAtTailStartRebasesTheTail(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("noncontig-in-tail")

	dir := t.TempDir()
	bus, s := openAgainstFake(t, f, id, dir)

	// Attach and land one clean event, so the server mark (1) sits exactly
	// at firstUnflushed-1 once the gapped tail is injected.
	publishTurnStart(bus, id, "turn:1", "attach and land seq 1")
	waitUntil(t, "seq 1 to be accepted", func() bool { return f.LastSeq(id) == 1 })

	// Inject a tail with a gap of its own, the shape a legacy or corrupted
	// outbox can hold: the next batch is [2,4] against mark 1.
	s.mu.Lock()
	if err := s.appendLocked([]WireEvent{
		{Seq: 2, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{V: 1, Turn: "turn:2"}, Text: "a"}},
		{Seq: 4, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{V: 1, Turn: "turn:2"}, Text: "b"}},
	}); err != nil {
		t.Fatalf("seed gapped tail: %v", err)
	}
	s.mu.Unlock()

	f.RejectAppendsWith(400, "Bad Request", "Non-contiguous batch: events[1] has seq 4, expected 3")
	s.triggerFlush()
	waitUntil(t, "the first push attempt", func() bool { return len(f.Batches()) >= 2 })
	f.ClearAppendRejection()

	waitUntil(t, "the renumbered tail to be accepted", func() bool { return f.LastSeq(id) == 3 })
	assertOneSessionNoFork(t, f, s)
	if next := s.LastSeq(); next != 3 {
		t.Errorf("projector LastSeq() = %d, want 3; the next event must continue the rebased stream", next)
	}
}

// TestFirstGapIndexPinsContiguityDecision pins the rebase decision helper
// directly: a clean tail from mark+1 reads as no gap, and a gap anywhere in
// the tail - not just at its head - names that index.
func TestFirstGapIndexPinsContiguityDecision(t *testing.T) {
	mk := func(seqs ...int64) []StoredEvent {
		out := make([]StoredEvent, len(seqs))
		for i, seq := range seqs {
			out[i] = StoredEvent{Seq: seq}
		}
		return out
	}
	if got := firstGapIndex(mk(2, 3, 4), 1); got != -1 {
		t.Errorf("firstGapIndex(clean tail) = %d, want -1", got)
	}
	if got := firstGapIndex(mk(2, 4), 1); got != 1 {
		t.Errorf("firstGapIndex(internal gap) = %d, want 1", got)
	}
	if got := firstGapIndex(mk(3, 4), 1); got != 0 {
		t.Errorf("firstGapIndex(head gap) = %d, want 0", got)
	}
	if got := firstGapIndex(nil, 1); got != -1 {
		t.Errorf("firstGapIndex(empty) = %d, want -1", got)
	}
}
