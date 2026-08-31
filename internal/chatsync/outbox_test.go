package chatsync

import (
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
