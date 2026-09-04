package chatsync

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// principalFixture is a chat.Session.SessionID stand-in. It is shaped like a
// real one (26-char base32) and is deliberately distinctive so a substring
// search over request targets, request bodies and every file on disk cannot
// match it by accident.
const principalFixture = "PRINCIPALLEAKPROBEAAAAAAAA"

// TestChatPrincipalNeverReachesTheWireOrDisk is the real gate on the identity
// separation.
//
// A defined LocalHandle type is NOT a barrier: LocalHandle(sess.SessionID)
// compiles, and so does any other conversion. The semgrep rule
// mivia.go.no-chat-principal-as-sync-handle raises the cost of writing that
// line; only this test proves the value did not travel. chat.Session.SessionID
// is the contextstate authorization subject, so a copy of it in a URL, in a
// request body or in a file this package writes is a leaked live capability.
func TestChatPrincipalNeverReachesTheWireOrDisk(t *testing.T) {
	fake := newFakeAPI(t)
	storeDir := t.TempDir()

	key := IdentityKey(principalFixture)
	identityDir := IdentityDir(storeDir)
	ident, err := LoadOrCreateIdentity(identityDir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, principalFixture, SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: fake.URL()},
		OutboxDir:       OutboxDirFor(storeDir, ident.LocalHandle),
		Identity:        IdentityRef{Dir: identityDir, Key: key},
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		CreateTitle:     "Leak Probe",
		HeartbeatPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: principalFixture,
		TurnID:    "turn:1",
		Detail:    "start",
		Timestamp: time.Now(),
	})
	bus.Publish(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: principalFixture,
		TurnID:    "turn:1",
		Timestamp: time.Now(),
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fake.LastSeq(syncSess.SessionID()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Guard against a vacuous pass: the session must actually have synced.
	if fake.LastSeq(syncSess.SessionID()) < 2 {
		t.Fatalf("the probe session synced %d events; it must sync to prove anything", fake.LastSeq(syncSess.SessionID()))
	}
	requests := fake.Requests()
	if len(requests) == 0 {
		t.Fatal("no requests recorded; this test proves nothing")
	}

	for i, req := range requests {
		if strings.Contains(req.Target, principalFixture) {
			t.Errorf("request %d (%s %s) carries the chat principal in its URL", i, req.Method, req.Target)
		}
		if bytes.Contains(req.Body, []byte(principalFixture)) {
			t.Errorf("request %d (%s %s) carries the chat principal in its body: %s", i, req.Method, req.Target, req.Body)
		}
	}

	assertNoPrincipalOnDisk(t, storeDir)
}

// assertNoPrincipalOnDisk walks every file the sync files under root - the
// identity record, the outbox events, the cursor - and asserts none of them
// stores the principal, in its name or in its bytes.
func assertNoPrincipalOnDisk(t *testing.T, root string) {
	t.Helper()
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(path, principalFixture) {
			t.Errorf("path %q carries the chat principal in a file or directory name", path)
		}
		if d.IsDir() {
			return nil
		}
		seen++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(principalFixture)) {
			t.Errorf("file %q stores the chat principal: %s", path, data)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", root, err)
	}
	if seen == 0 {
		t.Fatal("no files were written under the store dir; this test proves nothing")
	}
}

// TestOpenSessionPersistsTheRemoteSessionID pins the write-back. Without it
// the identity file never learns which remote session this run created, so the
// next run attaches to nothing, the server issues a fresh session at lastSeq 0
// while the local cursor survives, and every later append is a sequence-gap
// 400 - the failure this whole repair exists to end.
func TestOpenSessionPersistsTheRemoteSessionID(t *testing.T) {
	fake := newFakeAPI(t)
	storeDir := t.TempDir()

	key := IdentityKey("principal-writeback")
	identityDir := IdentityDir(storeDir)
	ident, err := LoadOrCreateIdentity(identityDir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if ident.RemoteSessionID != "" {
		t.Fatalf("a fresh identity already names a remote session: %q", ident.RemoteSessionID)
	}

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "principal-writeback", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: fake.URL()},
		OutboxDir:       OutboxDirFor(storeDir, ident.LocalHandle),
		Identity:        IdentityRef{Dir: identityDir, Key: key},
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		CreateTitle:     "Write-back Probe",
		HeartbeatPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	// The attach (and with it the write-back) runs on the first message. The
	// first projected seq is strictly after the write-back, so waiting for it
	// makes the read below deterministic.
	publishTurnStart(bus, "principal-writeback", "turn:1", "the message that attaches")
	waitForSeq(t, syncSess, 1)
	remoteID := syncSess.SessionID()
	if remoteID == "" {
		t.Fatal("SessionID() is empty after the first event attached; nothing was written back")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = syncSess.Stop(ctx)

	stored, err := LoadOrCreateIdentity(identityDir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity after run: %v", err)
	}
	if stored.RemoteSessionID != remoteID {
		t.Errorf("stored RemoteSessionID = %q, want %q", stored.RemoteSessionID, remoteID)
	}
	if stored.LocalHandle != ident.LocalHandle {
		t.Errorf("the write-back changed the local handle: %q then %q", ident.LocalHandle, stored.LocalHandle)
	}
}
