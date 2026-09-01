package chatsync

import (
	"path/filepath"
	"strings"
	"testing"
)

func appendNumbered(t *testing.T, ob *Outbox, texts ...string) {
	t.Helper()
	for i, text := range texts {
		ev := WireEvent{
			Seq:     int64(i) + 1,
			Type:    TypeTurnStarted,
			Payload: &TurnStartedPayload{Text: text},
		}
		if err := ob.Append(ev); err != nil {
			t.Fatalf("Append %q: %v", text, err)
		}
	}
}

func payloadTexts(events []StoredEvent) []string {
	out := make([]string, 0, len(events))
	for _, se := range events {
		out = append(out, string(se.Payload))
	}
	return out
}

// TestOutboxRebase_CursorFailureDoesNotSkipEvents pins the order of the two
// durable writes inside a rebase.
//
// A rebase renumbers the surviving unflushed events onto the server's mark and
// lowers the cursor to that mark. If the events file is renamed into place
// first and the cursor write then fails, the file is rebased while the cursor
// still holds the old, higher flushed seq. Every rebased event reads as
// already flushed, so the outbox skips it and the events are lost with no
// error anywhere near the loss. The cursor must therefore land first: if the
// rename then fails, the low cursor merely re-admits acknowledged events, and
// a resend keeps the stream contiguous where a skip does not.
func TestOutboxRebase_CursorFailureDoesNotSkipEvents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-rebase-order")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}

	appendNumbered(t, ob, "one", "two", "three", "four", "five")
	if err := ob.AdvanceCursor(2); err != nil {
		t.Fatalf("AdvanceCursor: %v", err)
	}

	// The server reports it is behind at seq 0, so the surviving events must
	// be renumbered onto that mark. The cursor write fails part way through.
	restore := failSyncFor(t, cursorFileName+".tmp")
	if _, err := ob.Rebase(0); err == nil {
		t.Fatal("Rebase returned nil with the cursor write failing; the failure is the premise of this test")
	}
	restore()
	_ = ob.Close()

	reopened, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	defer reopened.Close()

	events, err := reopened.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents after reopen: %v", err)
	}

	joined := strings.Join(payloadTexts(events), " ")
	for _, want := range []string{"three", "four", "five"} {
		if !strings.Contains(joined, want) {
			t.Errorf("event %q is gone from the outbox after a failed rebase; "+
				"it was never acknowledged and nothing reported the loss (queued payloads: %v)",
				want, payloadTexts(events))
		}
	}

	// Whatever survives must still be shippable: the first unflushed seq has
	// to sit exactly one above the cursor, and the run has to be contiguous.
	if len(events) == 0 {
		t.Fatal("the outbox is empty after a failed rebase")
	}
	if got, want := events[0].Seq, reopened.Cursor().FlushedSeq+1; got != want {
		t.Errorf("first unflushed seq = %d, want %d (cursor %d); the API rejects any other first seq",
			got, want, reopened.Cursor().FlushedSeq)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Errorf("unflushed seqs are not contiguous: %v", seqsOf(events))
			break
		}
	}
}
