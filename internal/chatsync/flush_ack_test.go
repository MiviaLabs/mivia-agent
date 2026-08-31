package chatsync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func ourEvents(seqs ...int64) []WireEvent {
	out := make([]WireEvent, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, WireEvent{
			Seq:  seq,
			Type: TypeTurnStarted,
			Payload: &TurnStartedPayload{
				Envelope: Envelope{V: 1, Turn: "turn:1", WriterID: "writer-me"},
				Text:     "ours",
			},
		})
	}
	return out
}

func seedOutbox(t *testing.T, evs ...WireEvent) *Outbox {
	t.Helper()
	ob, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"), 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = ob.Close() })
	if err := ob.Append(evs...); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	return ob
}

// TestFlushOutbox_ShortAckOverForeignBodiesIsRefused is the corruption the
// plan names (chat-sync-cli-slice.md:150-152): with ON CONFLICT DO NOTHING the
// first body wins, so `insertedCount: 0` reads as "already applied, all good"
// while the transcript on the server is somebody else's.
//
// Advancing the cursor on that answer discards the only copies of our events
// that exist.
func TestFlushOutbox_ShortAckOverForeignBodiesIsRefused(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("short-ack-foreign")
	f.AdvanceServerSeq(id, 3) // seqs 1..3 already hold bodies that are not ours

	c := fakeClient(t, f)
	ob := seedOutbox(t, ourEvents(1, 2, 3)...)

	_, err := FlushOutbox(context.Background(), c, ob, id)
	if err == nil {
		t.Fatal("FlushOutbox accepted insertedCount=0 over foreign bodies; our three events were silently discarded")
	}
	if !errors.Is(err, ErrTranscriptConflict) {
		t.Errorf("err = %v, want ErrTranscriptConflict", err)
	}
	if got := ob.Cursor().FlushedSeq; got != 0 {
		t.Errorf("cursor = %d, want 0; the cursor must not advance over events the server did not take", got)
	}
	unflushed, _ := ob.UnflushedEvents()
	if len(unflushed) != 3 {
		t.Errorf("unflushed = %d, want 3; our copies are the only ones that exist", len(unflushed))
	}
}

// TestFlushOutbox_ShortAckDoesNotAdoptTheServerHighWaterMark is the audit's
// executed evidence: the cursor was advanced to res.LastSeq, a mark that has
// nothing to do with the batch that was sent, so seqs 1-3 were lost.
func TestFlushOutbox_ShortAckDoesNotAdoptTheServerHighWaterMark(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("short-ack-highwater")
	f.AdvanceServerSeq(id, 100)

	c := fakeClient(t, f)
	ob := seedOutbox(t, ourEvents(1, 2, 3)...)

	_, _ = FlushOutbox(context.Background(), c, ob, id)
	if got := ob.Cursor().FlushedSeq; got == 100 {
		t.Fatalf("cursor = 100: the server's high-water mark was adopted as an ack for a batch of three events")
	}
	if got := ob.Cursor().FlushedSeq; got != 0 {
		t.Errorf("cursor = %d, want 0", got)
	}
}

// TestFlushOutbox_ReplayOfOurOwnBatchAdvances keeps the retry story working.
// After a lost ack the batch is re-sent byte-identically, the server answers
// insertedCount 0, and that IS all good - the server holds our bytes. Refusing
// it would wedge every recovery the retry policy depends on.
func TestFlushOutbox_ReplayOfOurOwnBatchAdvances(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("replay")

	c := fakeClient(t, f)
	ob := seedOutbox(t, ourEvents(1, 2, 3)...)

	if _, err := FlushOutbox(context.Background(), c, ob, id); err != nil {
		t.Fatalf("first FlushOutbox: %v", err)
	}
	// The ack was lost: the cursor never moved, so the same batch goes again.
	ob.mu.Lock()
	ob.cursor = Cursor{}
	ob.mu.Unlock()

	n, err := FlushOutbox(context.Background(), c, ob, id)
	if err != nil {
		t.Fatalf("replay FlushOutbox: %v", err)
	}
	if n != 0 {
		t.Errorf("insertedCount on replay = %d, want 0", n)
	}
	if got := ob.Cursor().FlushedSeq; got != 3 {
		t.Errorf("cursor after a verified replay = %d, want 3", got)
	}
}

// TestFlushOutbox_VerifiedReplayStopsAtWhatWeSent is the case that separates
// "advance to the last seq we sent" from "advance to res.LastSeq". The server
// holds our 1..3 AND another writer's 4..100. The replay verifies, so the
// cursor may move - but only to 3. Moving it to 100 would mark every local
// event up to seq 100 flushed although none of them was ever sent.
func TestFlushOutbox_VerifiedReplayStopsAtWhatWeSent(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("verified-replay-bounded")

	c := fakeClient(t, f)
	ob := seedOutbox(t, ourEvents(1, 2, 3)...)

	if _, err := FlushOutbox(context.Background(), c, ob, id); err != nil {
		t.Fatalf("first FlushOutbox: %v", err)
	}
	// Another writer extends the session far past this client's batch, and the
	// ack for the batch above was lost.
	f.AdvanceServerSeq(id, 100)
	ob.mu.Lock()
	ob.cursor = Cursor{}
	ob.mu.Unlock()

	if _, err := FlushOutbox(context.Background(), c, ob, id); err != nil {
		t.Fatalf("replay FlushOutbox: %v", err)
	}
	if got := ob.Cursor().FlushedSeq; got != 3 {
		t.Errorf("cursor = %d, want 3; the cursor must stop at the last seq this client sent, not at the session high-water mark", got)
	}
}

// TestFlushOutbox_FullAckAdvancesToWhatWeSent is the ordinary path, pinned so
// the verification does not cost a readback when nothing was contested.
func TestFlushOutbox_FullAckAdvancesToWhatWeSent(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("full-ack")

	c := fakeClient(t, f)
	ob := seedOutbox(t, ourEvents(1, 2, 3)...)

	n, err := FlushOutbox(context.Background(), c, ob, id)
	if err != nil {
		t.Fatalf("FlushOutbox: %v", err)
	}
	if n != 3 {
		t.Errorf("insertedCount = %d, want 3", n)
	}
	if got := ob.Cursor().FlushedSeq; got != 3 {
		t.Errorf("cursor = %d, want 3", got)
	}
}

// TestFlush_TranscriptConflictStopsSync wires the refusal to the settled
// terminal path. A server holding a different transcript at our seqs is not
// something a retry can fix.
func TestFlush_TranscriptConflictStopsSync(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("conflict-stop")

	bus, s := openAgainstFake(t, f, id, t.TempDir())
	// Only after attach: a second writer takes seqs 1..3 while this client is
	// still numbering from 0, so its first event collides with a foreign body.
	f.AdvanceServerSeq(id, 3)
	publishTurnStart(bus, id, "turn:1", "ours")

	waitUntil(t, "sync to stop on a transcript conflict", func() bool { return s.Stopped() })
	if reason := s.StopReason(); reason == "" {
		t.Error("sync stopped with no reason")
	}

	before := len(f.Batches())
	time.Sleep(300 * time.Millisecond)
	if after := len(f.Batches()); after != before {
		t.Errorf("the flusher sent %d more batches after the conflict stop", after-before)
	}
}
