package chatsync

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestEnsureAttachedPropagatesOpeningSeqError forces openingSeq's real
// Rebase call to fail (see session.go:411-413) and proves ensureAttached
// stops the session and reports it through OnStop, rather than leaving the
// worker stuck. Local outbox events ahead of the remote's reported seq is
// the normal resume case (Rebase runs on every such resume, not only on
// error); write permission is what is actually broken here.
func TestEnsureAttachedPropagatesOpeningSeqError(t *testing.T) {
	dir := t.TempDir()
	seed, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("seed OpenOutbox: %v", err)
	}
	if err := seed.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: TurnStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "t1"}}},
	); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
	// Make the cursor write Rebase performs fail: the directory itself
	// becomes unwritable, not just one file, so any recreated cursor file
	// fails the same way.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, srv := newRecordingServer(t, "sess-attach-gap")

	stopped := make(chan string, 1)
	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-attach-gap", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       dir,
		MaxUnflushed:    100,
		CreateTitle:     "Attach Gap",
		HeartbeatPeriod: 10 * time.Minute,
		OnStop:          func(reason string) { stopped <- reason },
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = syncSess.Stop(ctx)
	})

	publishTurnStart(bus, "sess-attach-gap", "turn:1", "triggers the deferred attach")

	select {
	case reason := <-stopped:
		if reason == "" {
			t.Fatal("OnStop reason is empty, want it to explain the rebase failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ensureAttached's openingSeq failure to stop the session")
	}
}

// TestEnsureAttachedPropagatesReconcileDanglingError forces
// reconcileDangling's own Append call to fail (via scriptedAppender, the
// same seam session_seq_test.go's tests use) after a normal, successful
// attach: a dangling tool call left in events.jsonl by an earlier
// interrupted process is what makes reconcileDangling attempt to write a
// synthesized closing event in the first place.
func TestEnsureAttachedPropagatesReconcileDanglingError(t *testing.T) {
	dir := t.TempDir()
	seed, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("seed OpenOutbox: %v", err)
	}
	if err := seed.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: TurnStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-dangling"}}},
		WireEvent{Seq: 2, Type: TypeToolStarted, Payload: ToolStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-dangling"}, ToolCallID: "call-1", Name: "bash"}},
	); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	_, srv := newRecordingServer(t, "sess-reconcile-gap")

	stopped := make(chan string, 1)
	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-reconcile-gap", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       dir,
		MaxUnflushed:    100,
		CreateTitle:     "Reconcile Gap",
		HeartbeatPeriod: 10 * time.Minute,
		OnStop:          func(reason string) { stopped <- reason },
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = syncSess.Stop(ctx)
	})

	// Attach and openingSeq must succeed normally (they use s.outbox/
	// s.client, not s.appender): only reconcileDangling's own Append is
	// broken here.
	failing := interceptAppends(syncSess)
	failing.fail.Store(true)

	publishTurnStart(bus, "sess-reconcile-gap", "turn:new", "triggers the deferred attach")

	select {
	case reason := <-stopped:
		if reason == "" {
			t.Fatal("OnStop reason is empty, want it to explain the reconcile-dangling failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ensureAttached's reconcileDangling failure to stop the session")
	}
}

// TestStartRunnersDefaultsToWaitingInputStatus covers startRunners' status
// fallback (session_attach.go:91-92): a session attaching for the first
// time (currentStatus never set by any HandleEvent yet) must still start its
// heartbeat under a real status string, not an empty one.
func TestStartRunnersDefaultsToWaitingInputStatus(t *testing.T) {
	_, srv := newRecordingServer(t, "sess-status-default")
	client, err := NewClient(testTokenProvider, ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	outbox, err := OpenOutbox(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })

	s := newSyncSession("sess-status-default", client, outbox, SessionOptions{HeartbeatPeriod: time.Minute}, CreateSessionParams{})
	s.running.Store(true)
	// currentStatus is deliberately left at its zero value: newSyncSession
	// sets it to "waiting_input" already in production, but the fallback
	// exists precisely so a caller that reaches here with an empty status
	// (this direct construction) still gets a real value, not "".
	s.statusMu.Lock()
	s.currentStatus = ""
	s.statusMu.Unlock()

	s.startRunners(context.Background(), "sess-status-default")
	t.Cleanup(func() { s.stopRunners(context.Background()) })

	hb := s.statusRunner()
	if hb == nil {
		t.Fatal("startRunners did not start a heartbeat runner")
	}
}
