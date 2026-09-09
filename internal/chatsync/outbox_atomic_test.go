package chatsync

import (
	"os"
	"path/filepath"
	"testing"
)

// unmarshalablePayload is a payload json.Marshal cannot encode. It stands in
// for any per-event encode or write failure that strikes PART WAY through a
// multi-event batch, which is the case Append must not leave half-written.
type unmarshalablePayload struct {
	Ch chan int `json:"ch"`
}

func failingBatch() []WireEvent {
	return []WireEvent{
		{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "one"}},
		{Seq: 2, Type: TypeTurnStarted, Payload: &unmarshalablePayload{Ch: make(chan int)}},
		{Seq: 3, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "three"}},
	}
}

func goodBatch() []WireEvent {
	return []WireEvent{
		{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "one"}},
		{Seq: 2, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "two"}},
		{Seq: 3, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "three"}},
	}
}

// TestOutboxAppend_MidBatchFailureLeavesNoPartialBatch proves Append is
// all-or-nothing across a batch.
//
// A failure on event 2 of 3 used to leave event 1 on disk with `unflushed`
// un-incremented. The batch reads as never stored, so the seq counter rolls
// back and reissues those seqs, while the file still holds the first record.
func TestOutboxAppend_MidBatchFailureLeavesNoPartialBatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-atomic")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	if err := ob.Append(failingBatch()...); err == nil {
		t.Fatal("Append with an unencodable event returned nil; the failure is the premise of this test")
	}

	unflushed, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 0 {
		t.Errorf("the file holds %d records after a failed batch, want 0 "+
			"(a partial batch on disk is a record the outbox does not know it has)", len(unflushed))
	}

	data, err := os.ReadFile(filepath.Join(dir, eventsFileName))
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("events.jsonl holds %d bytes after a failed batch, want 0; content: %s", len(data), data)
	}
}

// TestOutboxAppend_FailedBatchDoesNotDuplicateReissuedSeqs is the consequence
// the server sees. appendLocked rolls the seq counter back on failure, so the
// same seqs are reissued. A partial record left behind then gives the file two
// events with the same seq, and the server's contiguity check rejects the batch
// - wedging the stream permanently.
func TestOutboxAppend_FailedBatchDoesNotDuplicateReissuedSeqs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-atomic-dup")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	if err := ob.Append(failingBatch()...); err == nil {
		t.Fatal("Append with an unencodable event returned nil; the failure is the premise of this test")
	}

	// The projector rolled the counter back, so the same seqs are reissued.
	if err := ob.Append(goodBatch()...); err != nil {
		t.Fatalf("Append after a rolled-back batch: %v", err)
	}

	unflushed, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	seen := map[int64]int{}
	for _, se := range unflushed {
		seen[se.Seq]++
	}
	for seq, n := range seen {
		if n > 1 {
			t.Errorf("seq %d appears %d times in events.jsonl; the server rejects a duplicate seq and the stream never recovers", seq, n)
		}
	}
	if len(unflushed) != 3 {
		t.Errorf("unflushed len = %d, want 3", len(unflushed))
	}
}

// TestOutboxAppend_FailedBatchDoesNotAdvanceMaxSeq covers the other half of the
// same accounting: maxSeq seeds the projector's starting sequence on attach, so
// a seq that was never stored must not raise it.
func TestOutboxAppend_FailedBatchDoesNotAdvanceMaxSeq(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-atomic-maxseq")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	if err := ob.Append(failingBatch()...); err == nil {
		t.Fatal("Append with an unencodable event returned nil; the failure is the premise of this test")
	}
	if got := ob.MaxSeq(); got != 0 {
		t.Errorf("MaxSeq() after a failed batch = %d, want 0 (no event was stored)", got)
	}
}
