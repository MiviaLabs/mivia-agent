package chatsync

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOutboxAppendAndReplayUnflushed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-outbox")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	// Initial unflushed: empty
	unflushed, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 0 {
		t.Fatalf("initial unflushed len = %d, want 0", len(unflushed))
	}

	// Append 3 events
	ev1 := WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "hi"}}
	ev2 := WireEvent{Seq: 2, Type: TypeAssistantMessage, Payload: &AssistantMessagePayload{Text: "hello"}}
	ev3 := WireEvent{Seq: 3, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Reason: "completed"}}

	if err := ob.Append(ev1, ev2, ev3); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Replay all 3
	unflushed, err = ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 3 {
		t.Fatalf("unflushed len = %d, want 3", len(unflushed))
	}
	if unflushed[0].Seq != 1 || unflushed[1].Seq != 2 || unflushed[2].Seq != 3 {
		t.Errorf("unflushed seqs = %d, %d, %d; want 1, 2, 3", unflushed[0].Seq, unflushed[1].Seq, unflushed[2].Seq)
	}

	// Advance cursor to 2 (after ack from server)
	if err := ob.AdvanceCursor(2); err != nil {
		t.Fatalf("AdvanceCursor: %v", err)
	}

	// Replay: only seq 3 remains unflushed
	unflushed, err = ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents after advance: %v", err)
	}
	if len(unflushed) != 1 {
		t.Fatalf("unflushed len after advance = %d, want 1", len(unflushed))
	}
	if unflushed[0].Seq != 3 {
		t.Errorf("unflushed[0].Seq = %d, want 3", unflushed[0].Seq)
	}
}

func TestOutboxCursorAtomicAndPersistent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-outbox")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}

	ev1 := WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "hi"}}
	_ = ob.Append(ev1)
	if err := ob.AdvanceCursor(1); err != nil {
		t.Fatalf("AdvanceCursor: %v", err)
	}
	cur := ob.Cursor()
	if cur.FlushedSeq != 1 || cur.FlushedAt.IsZero() {
		t.Fatalf("cur = %+v, want FlushedSeq=1", cur)
	}

	// Close and reopen
	if err := ob.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ob2, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox re-open: %v", err)
	}
	defer ob2.Close()

	cur2 := ob2.Cursor()
	if cur2.FlushedSeq != 1 {
		t.Errorf("reopened cursor FlushedSeq = %d, want 1", cur2.FlushedSeq)
	}
	unflushed, err := ob2.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 0 {
		t.Errorf("unflushed after reload = %d, want 0", len(unflushed))
	}
}

func TestOutboxLockingMutualExclusion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-outbox")
	ob1, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox 1: %v", err)
	}

	// Second open on same directory must fail with ErrOutboxLocked
	ob2, err := OpenOutbox(dir, 100)
	if err != ErrOutboxLocked {
		if ob2 != nil {
			ob2.Close()
		}
		t.Fatalf("expected ErrOutboxLocked, got err = %v", err)
	}

	// After closing ob1, ob3 must succeed
	if err := ob1.Close(); err != nil {
		t.Fatalf("Close ob1: %v", err)
	}

	ob3, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox 3: %v", err)
	}
	ob3.Close()
}

func TestOutboxCapacityOverflow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-outbox")
	ob, err := OpenOutbox(dir, 3) // max 3 unflushed
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	ev1 := WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "1"}}
	ev2 := WireEvent{Seq: 2, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "2"}}
	ev3 := WireEvent{Seq: 3, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "3"}}
	ev4 := WireEvent{Seq: 4, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "4"}}

	if err := ob.Append(ev1, ev2, ev3); err != nil {
		t.Fatalf("Append 3: %v", err)
	}

	// 4th event exceeds capacity
	if err := ob.Append(ev4); err != ErrOutboxOverflow {
		t.Fatalf("expected ErrOutboxOverflow, got err = %v", err)
	}

	// Advance cursor to 2 (unflushed count drops to 1)
	if err := ob.AdvanceCursor(2); err != nil {
		t.Fatalf("AdvanceCursor: %v", err)
	}

	// Now append ev4 succeeds
	if err := ob.Append(ev4); err != nil {
		t.Fatalf("Append ev4 after advance: %v", err)
	}
}

func TestOutbox_ByteIdenticalPayloadReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-raw-payload")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	// Use exact JSON with custom ordering and types
	rawJSON := []byte(`{"writer_id":"w-1","custom_field":123.456,"nested":{"b":1,"a":2}}`)
	ev := WireEvent{
		Seq:     1,
		Type:    TypeTurnStarted,
		Payload: json.RawMessage(rawJSON),
	}

	if err := ob.Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	unflushed, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 1 {
		t.Fatalf("unflushed len = %d, want 1", len(unflushed))
	}

	// Payload must be byte-identical to original JSON
	gotPayload := []byte(unflushed[0].Payload)
	if !bytes.Equal(gotPayload, rawJSON) {
		t.Errorf("payload bytes differ:\n got:  %s\n want: %s", gotPayload, rawJSON)
	}
}

func TestOutbox_ResetForFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-fork-reset")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	// Append 5 events, advance cursor to 2
	_ = ob.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "1"}},
		WireEvent{Seq: 2, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Reason: "2"}},
		WireEvent{Seq: 3, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "3"}},
		WireEvent{Seq: 4, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Reason: "4"}},
		WireEvent{Seq: 5, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "5"}},
	)
	_ = ob.AdvanceCursor(2)

	// Reset for fork: unflushed events (3, 4, 5) should be resequenced to 1, 2, 3
	count, err := ob.ResetForFork()
	if err != nil {
		t.Fatalf("ResetForFork: %v", err)
	}
	if count != 3 {
		t.Errorf("ResetForFork count = %d, want 3", count)
	}

	if ob.Cursor().FlushedSeq != 0 {
		t.Errorf("cursor after fork = %d, want 0", ob.Cursor().FlushedSeq)
	}
	if ob.MaxSeq() != 3 {
		t.Errorf("maxSeq after fork = %d, want 3", ob.MaxSeq())
	}

	unflushed, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 3 {
		t.Fatalf("unflushed len = %d, want 3", len(unflushed))
	}
	for i, ev := range unflushed {
		if ev.Seq != int64(i+1) {
			t.Errorf("unflushed[%d].Seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestOutbox_ResetForFork_Empty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat-sync-fork-empty")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer ob.Close()

	count, err := ob.ResetForFork()
	if err != nil {
		t.Fatalf("ResetForFork empty: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}
