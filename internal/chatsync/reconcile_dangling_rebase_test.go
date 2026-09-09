package chatsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestReconcileDangling_SynthesizedTurnFailedSurvivesAttachRebase closes a
// coverage gap left by the reconcileDangling bare-turn fix
// (TestReconcileDangling_ClosesBareOpenTurnWithNoToolCalls): nothing proved
// that fix keeps working once the SAME attach also has to renumber the
// outbox onto the server's mark (chat-sync-cli-slice.md:86, :253 - the
// settled rebase-onto-server-mark rule TestAttachRebasesOntoTheServerMarkNotTheLocalMax
// pins for a CLOSED turn).
//
// The fixture combines both shapes at once: seedCrashedDanglingOutbox seeds
// an outbox with 3 acked-but-orphaned events and 2 unflushed events on a
// turn that is NEVER closed - a genuinely dangling open turn sitting on top
// of unsent events, the exact combination attach_authority_test.go's
// seedCrashedOutbox stopped exercising once it started closing its seeded
// turn (see .../review/02-test-review.md section 4).
//
// ensureAttached runs openingSeq's Rebase(0) BEFORE reconcileDangling reads
// events.jsonl, so the dangling turn's own events are already renumbered by
// the time reconcileDangling appends its synthesized turn.failed. This test
// asserts that ordering holds end to end: the synthesized event lands with
// the correct turn id, the very next seq (no gap, no collision with the
// rebased tail), and the server-side transcript is contiguous from 1 with
// exactly one turn.failed record.
func TestReconcileDangling_SynthesizedTurnFailedSurvivesAttachRebase(t *testing.T) {
	f := newFakeAPI(t)
	storeDir := t.TempDir()

	key := IdentityKey("principal-dangling-rebase")
	ident, err := LoadOrCreateIdentity(IdentityDir(storeDir), key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	outboxDir := OutboxDirFor(storeDir, ident.LocalHandle)

	seedCrashedDanglingOutbox(t, outboxDir)

	// OpenSession arms sync with nothing on the wire; the attach - and with
	// it the rebase onto the server's mark and reconcileDangling - runs on
	// the first message, exactly as in production.
	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "principal-dangling-rebase", SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: f.URL()},
		// The remote session this outbox was flushed to is gone, so attach
		// creates a fresh one at server seq 0 - the same shape
		// TestAttachRebasesOntoTheServerMarkNotTheLocalMax uses.
		RemoteSessionID: "fake-session-gone",
		OutboxDir:       outboxDir,
		LocalHandle:     ident.LocalHandle,
		Identity:        IdentityRef{Dir: IdentityDir(storeDir), Key: key},
		MaxUnflushed:    100,
		CreateTitle:     "Dangling Rebase",
		HeartbeatPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, "principal-dangling-rebase", "turn:new", "after recovery")

	// Rebase renumbers the 2 unflushed seeded events onto the fresh server
	// mark (0 -> 1,2). reconcileDangling then appends turn.failed for the
	// still-open "turn:1" at seq 3. The new turn.started this test just
	// published lands last, at seq 4. If the fix regressed under rebase -
	// wrong seq, a dropped event, or a collision - LastSeq never reaches 4.
	waitForSeq(t, syncSess, 4)
	if got := syncSess.LastSeq(); got != 4 {
		t.Fatalf("projector LastSeq() = %d, want 4 (2 rebased + 1 synthesized turn.failed + 1 new)", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if syncSess.Stopped() {
		t.Fatalf("sync stopped: %s", syncSess.StopReason())
	}

	assertDanglingRebaseServerState(t, f, syncSess.SessionID())
}

// assertDanglingRebaseServerState checks the server-side transcript left by
// TestReconcileDangling_SynthesizedTurnFailedSurvivesAttachRebase: contiguous
// from 1, exactly 4 events, and exactly one synthesized turn.failed for the
// dangling turn (not the new one), with no gap or duplicate around it.
func assertDanglingRebaseServerState(t *testing.T, f *fakeAPI, sessionID string) {
	t.Helper()
	assertContiguousTranscript(t, f, sessionID, 4)

	stored := f.Events(sessionID)
	closing := stored[2]
	if closing.Type != TypeTurnFailed {
		t.Fatalf("event at seq %d has type %q, want %q (the synthesized closing event)", closing.Seq, closing.Type, TypeTurnFailed)
	}
	var payload TurnFailedPayload
	if err := json.Unmarshal(closing.Payload, &payload); err != nil {
		t.Fatalf("unmarshal turnFailed payload: %v", err)
	}
	if payload.Turn != "turn:1" {
		t.Errorf("synthesized turn.failed.turn = %q, want %q (the dangling turn from the seed, not the new one)", payload.Turn, "turn:1")
	}

	// Exactly one turn.failed reached the server: the rebase path must not
	// duplicate the synthesized close, and reconcileDangling must not run
	// twice against the same attach.
	failedCount := 0
	for _, se := range stored {
		if se.Type == TypeTurnFailed {
			failedCount++
		}
	}
	if failedCount != 1 {
		t.Errorf("server holds %d turn.failed events, want exactly 1", failedCount)
	}
}

// seedCrashedDanglingOutbox writes the same acked-plus-unsent shape
// seedCrashedOutbox (attach_authority_test.go) does - seqs 1..3 acknowledged
// by a remote session that no longer exists, 4..5 assigned locally and never
// sent - but deliberately leaves "turn:1" open. seedCrashedOutbox always
// closes its seeded turn so the rebase tests it feeds are not about turn
// state at all; this helper is the missing counterpart that keeps the turn
// genuinely dangling, so reconcileDangling has something real to close on
// this same attach.
func seedCrashedDanglingOutbox(t *testing.T, outboxDir string) {
	t.Helper()
	ob, err := OpenOutbox(outboxDir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	if err := ob.Append(ourEvents(1, 2, 3, 4, 5)...); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := ob.AdvanceCursor(3); err != nil {
		t.Fatalf("seed AdvanceCursor: %v", err)
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}
