package chatsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestServerAheadAdoptsOurPersistedWriterAndForksAnyOtherOne is the cross-run
// case REVIEW CHANGE 8 turns on.
//
// Server-ahead has at least four causes and only one is a second writer: a
// lost ack, a crash between the 200 and the fsync, concurrent cursor writers,
// and an org admin appending. Defaulting to fork converts the most common
// transient failure into permanent data loss, on a schedule - every API
// deploy severs in-flight POSTs after the server committed.
//
// The writer id is what tells the four apart, and it must be the SAME value
// across restarts. A fresh random per run would make every restart read its
// own previous run's events as foreign: end the remote session, fork, and
// strand the transcript. That is the second row of this table, and it is why
// the writer id is persisted rather than minted per run.
func TestServerAheadAdoptsOurPersistedWriterAndForksAnyOtherOne(t *testing.T) {
	cases := []struct {
		name string
		// plantAs is the writer id stamped on the events the client never saw.
		// Empty means "our own persisted id".
		plantAs   string
		wantFork  bool
		wantEnded bool
	}{
		{name: "our own persisted writer id adopts", plantAs: "", wantFork: false},
		{name: "any other writer forks", plantAs: "a-different-writer", wantFork: true, wantEnded: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			storeDir := t.TempDir()
			const principal = "principal-writer-identity"

			ident, err := LoadOrCreateIdentity(IdentityDir(storeDir), IdentityKey(principal))
			if err != nil {
				t.Fatalf("LoadOrCreateIdentity: %v", err)
			}

			firstID := runWithPersistedWriter(t, f, storeDir, principal, "turn:1")

			// The state a lost ack leaves: the server holds events past our
			// cursor that this client has no record of.
			plantAs := tc.plantAs
			if plantAs == "" {
				plantAs = ident.WriterID
			}
			f.AdvanceServerSeqAs(firstID, f.LastSeq(firstID)+2, plantAs)

			secondID := runWithPersistedWriter(t, f, storeDir, principal, "turn:2")

			if tc.wantFork {
				if secondID == firstID {
					t.Fatalf("a foreign writer must fork, but the restart stayed on %q", secondID)
				}
				if n := createCount(f); n != 2 {
					t.Errorf("create count = %d, want 2 (the original plus the fork)", n)
				}
			} else {
				if secondID != firstID {
					t.Fatalf("our own writer id must ADOPT, but the restart forked from %q to %q; every restart would strand its transcript", firstID, secondID)
				}
				if n := createCount(f); n != 1 {
					t.Errorf("create count = %d, want 1; the restart minted a session it did not need", n)
				}
			}
			if got := endCalled(f, firstID); got != tc.wantEnded {
				t.Errorf("EndSession called = %v, want %v", got, tc.wantEnded)
			}
		})
	}
}

// endCalled reports whether the fake was asked to end a session.
func endCalled(f *fakeAPI, id string) bool {
	for _, req := range f.Requests() {
		if req.Method == "POST" && strings.HasSuffix(req.Target, "/v1/chat-sessions/"+id+"/end") {
			return true
		}
	}
	return false
}

// runWithPersistedWriter is one process lifetime wired the way production
// wires it: every identity field, INCLUDING the writer id, comes from the
// persisted record rather than from a literal in this test.
func runWithPersistedWriter(t *testing.T, f *fakeAPI, storeDir, principal, turnID string) string {
	t.Helper()
	key := IdentityKey(principal)
	ident, err := LoadOrCreateIdentity(IdentityDir(storeDir), key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, principal, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{WriterID: ident.WriterID},
		RemoteSessionID:  ident.RemoteSessionID,
		OutboxDir:        OutboxDirFor(storeDir, ident.LocalHandle),
		LocalHandle:      ident.LocalHandle,
		Identity:         IdentityRef{Dir: IdentityDir(storeDir), Key: key},
		MaxUnflushed:     100,
		CreateTitle:      "Writer Identity",
		HeartbeatPeriod:  time.Hour,
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
