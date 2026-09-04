package chatsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// createCount is how many remote sessions the fake was asked to mint.
func createCount(f *fakeAPI) int {
	n := 0
	for _, req := range f.Requests() {
		if req.Method == "POST" && req.Target == "/v1/chat-sessions" {
			n++
		}
	}
	return n
}

// TestAttachRebasesOntoTheServerMarkNotTheLocalMax is the settled rule
// (chat-sync-cli-slice.md:86 and :253): serverLastSeq is authoritative, NEVER
// max(local, server).
//
// The state modelled here is the one that started this repair. A previous run
// flushed seqs 1..3 to a remote session and then appended 4..6 that it never
// sent. The remote session is gone, so attach creates a fresh one at lastSeq
// 0. Taking the local max means the next append opens at 4 against a server
// expecting 1: a sequence gap the server rejects with a 400 on every later
// append, for the rest of the process lifetime. Taking the server mark
// renumbers the surviving unflushed events onto it and the stream is
// contiguous again.
func TestAttachRebasesOntoTheServerMarkNotTheLocalMax(t *testing.T) {
	f := newFakeAPI(t)
	storeDir := t.TempDir()

	key := IdentityKey("principal-rebase")
	ident, err := LoadOrCreateIdentity(IdentityDir(storeDir), key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	outboxDir := OutboxDirFor(storeDir, ident.LocalHandle)

	seedCrashedOutbox(t, outboxDir)

	// OpenSession arms sync with nothing on the wire; the attach (and with it the
	// renumbering onto the server's mark) runs on the FIRST message, exactly as
	// in production. The event below is that message.
	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "principal-rebase", SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: f.URL()},
		// The remote session this outbox was flushed to is gone.
		RemoteSessionID: "fake-session-gone",
		OutboxDir:       outboxDir,
		LocalHandle:     ident.LocalHandle,
		Identity:        IdentityRef{Dir: IdentityDir(storeDir), Key: key},
		MaxUnflushed:    100,
		CreateTitle:     "Rebase",
		HeartbeatPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// The crisp statement of the rule, observable once the first message has
	// been through the attach: the counter opens at the SERVER mark (0, a
	// freshly created session) plus the three events the outbox still holds,
	// never at the local high-water mark of 6 - so the new event takes seq 4,
	// not 7.
	publishTurnStart(bus, "principal-rebase", "turn:new", "after recovery")
	waitForSeq(t, syncSess, 4)
	if got := syncSess.LastSeq(); got != 4 {
		t.Fatalf("projector opened at the server mark 0 + 3 recovered events, so the new event takes 4; got %d (a local-max open would take 7)", got)
	}
	baseSeq := syncSess.LastSeq()

	// A second event on top of the recovered outbox. This is where taking the
	// local max is not merely wasteful: the projector keeps numbering from 6
	// while the outbox has been renumbered onto the server's mark, so the next
	// append is another gap at the SAME server mark - which the rebase loop
	// guard reads as a rebase that moved nothing and poisons sync.
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "principal-rebase",
		TurnID:    "turn:new-2",
		Detail:    "after recovery, again",
		Timestamp: time.Now(),
	})
	waitUntilSeqPast(t, syncSess, baseSeq)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if syncSess.Stopped() {
		t.Fatalf("sync stopped: %s", syncSess.StopReason())
	}
	assertContiguousTranscript(t, f, syncSess.SessionID(), 5)
}

// seedCrashedOutbox writes the state a crashed run leaves behind: seqs 1..3
// acknowledged by a remote session that no longer exists, and 4..6 assigned
// locally and never sent.
func seedCrashedOutbox(t *testing.T, outboxDir string) {
	t.Helper()
	ob, err := OpenOutbox(outboxDir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	if err := ob.Append(ourEvents(1, 2, 3, 4, 5, 6)...); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := ob.AdvanceCursor(3); err != nil {
		t.Fatalf("seed AdvanceCursor: %v", err)
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}

// assertContiguousTranscript checks the server holds exactly want events
// numbered 1..want, and that no rejected round trip was needed to get there:
// the FIRST batch it saw already opened at its own next seq.
func assertContiguousTranscript(t *testing.T, f *fakeAPI, remoteID string, want int) {
	t.Helper()
	stored := f.Events(remoteID)
	if len(stored) != want {
		t.Fatalf("server holds %d events, want %d", len(stored), want)
	}
	for i, se := range stored {
		if wantSeq := int64(i + 1); se.Seq != wantSeq {
			t.Errorf("stored event %d has seq %d, want %d; the stream must be contiguous from the server's mark", i, se.Seq, wantSeq)
		}
	}
	if got := f.LastSeq(remoteID); got != int64(want) {
		t.Errorf("server lastSeq = %d, want %d", got, want)
	}
	batches := f.Batches()
	if len(batches) == 0 || len(batches[0]) == 0 {
		t.Fatal("the fake received no append batch")
	}
	if got := batches[0][0].Seq; got != 1 {
		t.Errorf("first batch opened at seq %d, want 1; a batch that opens past the server mark is the sequence-gap 400", got)
	}
}

// TestAttachAfterRestartReattachesInsteadOfCreating is the cross-run half. A
// restart that cannot find its remote session creates a new one at lastSeq 0
// while the local cursor survives - the permanent sequence-gap 400. The
// persisted identity is what closes it.
func TestAttachAfterRestartReattachesInsteadOfCreating(t *testing.T) {
	f := newFakeAPI(t)
	storeDir := t.TempDir()
	const principal = "principal-restart"

	firstID := runSyncOnce(t, f, storeDir, principal, "turn:1")
	if n := createCount(f); n != 1 {
		t.Fatalf("first run created %d remote sessions, want 1", n)
	}

	secondID := runSyncOnce(t, f, storeDir, principal, "turn:2")

	if secondID != firstID {
		t.Errorf("restart attached to %q, want the persisted %q", secondID, firstID)
	}
	if n := createCount(f); n != 1 {
		t.Errorf("restart created a second remote session (%d creates); the first transcript is orphaned", n)
	}
	if got := f.LastSeq(firstID); got < 2 {
		t.Errorf("remote session holds lastSeq %d, want both runs' events", got)
	}
	for _, req := range f.Requests() {
		if strings.Contains(req.Target, principal) {
			t.Errorf("request %s carries the chat principal", req.Target)
		}
	}
}

// runSyncOnce is one process lifetime: resolve the persisted identity, open,
// publish one turn, stop. It returns the remote session id that run used.
func runSyncOnce(t *testing.T, f *fakeAPI, storeDir, principal, turnID string) string {
	t.Helper()
	key := IdentityKey(principal)
	ident, err := LoadOrCreateIdentity(IdentityDir(storeDir), key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, principal, SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: f.URL()},
		RemoteSessionID: ident.RemoteSessionID,
		OutboxDir:       OutboxDirFor(storeDir, ident.LocalHandle),
		LocalHandle:     ident.LocalHandle,
		Identity:        IdentityRef{Dir: IdentityDir(storeDir), Key: key},
		MaxUnflushed:    100,
		CreateTitle:     "Restart",
		HeartbeatPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	baseSeq := s.LastSeq()
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: principal,
		TurnID:    turnID,
		Detail:    "hello",
		Timestamp: time.Now(),
	})
	waitUntilSeqPast(t, s, baseSeq)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	return s.SessionID()
}

// waitUntilSeqPast blocks until the worker has projected an event past base,
// so Stop flushes a turn rather than racing the bus delivery. The baseline
// matters: a resumed session opens with a non-zero counter, so "greater than
// zero" is already true before the new event is projected.
func waitUntilSeqPast(t *testing.T, s *SyncSession, base int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.LastSeq() > base {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("LastSeq() = %d, want past %d within 3s", s.LastSeq(), base)
}
