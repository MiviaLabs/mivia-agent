package chatsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestReconcileDangling_ClosesBareOpenTurnWithNoToolCalls locks in the case
// where a plain question-and-answer turn has no tool or subagent calls at
// all. The process can still die between turn.started and turn.ended, so
// scanDanglingEvents returns a non-empty openTurn with every dangling slice
// empty. reconcileDangling must still synthesize a turn.failed event for
// that turn - otherwise the durable record shows the turn permanently open,
// even though nothing is ever going to finish it.
func TestReconcileDangling_ClosesBareOpenTurnWithNoToolCalls(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()

	// Append only a turn.started event - no tool, no subagent, no terminal
	// event. This is what a bare Q&A turn looks like on disk when the
	// process is killed before the answer finishes.
	err = outbox.Append(
		WireEvent{
			Seq:  1,
			Type: TypeTurnStarted,
			Payload: TurnStartedPayload{
				Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-bare"},
			},
		},
	)
	if err != nil {
		t.Fatalf("Append turn.started: %v", err)
	}

	session := &SyncSession{
		outbox:    outbox,
		appender:  outbox,
		projector: NewProjector("chat-test", 1, ProjectorOptions{WriterID: "cli-writer"}),
		flushCh:   make(chan struct{}, 1),
	}

	ctx := context.Background()
	if err := session.reconcileDangling(ctx); err != nil {
		t.Fatalf("reconcileDangling: %v", err)
	}

	// Projector seq must advance for the single synthesized turn.failed
	// event. If it stays at 1, no closing event was appended and the turn
	// is still open as far as the durable record is concerned.
	lastSeq := session.projector.LastSeq()
	if lastSeq != 2 {
		t.Fatalf("projector.LastSeq() = %d, want 2 (turn.failed synthesized)", lastSeq)
	}

	unflushed, err := outbox.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 2 {
		t.Fatalf("len(unflushed) = %d, want 2", len(unflushed))
	}

	if unflushed[1].Seq != 2 || unflushed[1].Type != TypeTurnFailed {
		t.Errorf("event 2 = seq %d, type %s; want seq 2, type %s",
			unflushed[1].Seq, unflushed[1].Type, TypeTurnFailed)
	}
	var turnFailed TurnFailedPayload
	if err := json.Unmarshal(unflushed[1].Payload, &turnFailed); err != nil {
		t.Fatalf("unmarshal turnFailed: %v", err)
	}
	if turnFailed.Turn != "turn-bare" {
		t.Errorf("turnFailed.Turn = %q, want %q", turnFailed.Turn, "turn-bare")
	}
}
