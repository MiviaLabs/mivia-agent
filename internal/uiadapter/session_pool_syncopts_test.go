package uiadapter

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPoolSyncOptionsCarriesStreamAssistant pins that sync.stream_assistant
// reaches the projector. ProjectorOptions.StreamAssistant was set nowhere in
// production, which is the dead-option class: the key parses, normalizes and
// documents, and the runtime does the default thing with no error.
func TestPoolSyncOptionsCarriesStreamAssistant(t *testing.T) {
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "s1"}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.ResolvedSync{StreamAssistant: want}}
		got := poolSyncOptions(sess, "s1", wsRoot, res, nil).ProjectorOptions.StreamAssistant
		if got != want {
			t.Errorf("StreamAssistant = %v, want %v", got, want)
		}
	}
}

// TestPoolSyncOptionsKeepsThePrincipalOutOfTheOutboxPath is the TUI half of
// the same contract the plain-CLI surface holds
// (clichat.TestCLISyncOptionsKeepsThePrincipalOutOfTheOutboxPath). The two
// surfaces build the same layout, and a layout that drifts between them
// orphans one surface's cursor.
func TestPoolSyncOptionsKeepsThePrincipalOutOfTheOutboxPath(t *testing.T) {
	const principal = "PRINCIPALTUIAAAAAAAAAAAAAA"
	sess := &chat.Session{SessionID: principal}

	opts := poolSyncOptions(sess, principal, t.TempDir(), &config.Resolved{}, nil)

	if strings.Contains(opts.OutboxDir, principal) {
		t.Errorf("OutboxDir = %q names the chat principal", opts.OutboxDir)
	}
	if opts.LocalHandle == "" {
		t.Fatal("no local handle was resolved")
	}
	if !strings.Contains(opts.OutboxDir, string(opts.LocalHandle)) {
		t.Errorf("OutboxDir = %q is not named after the local handle %q", opts.OutboxDir, opts.LocalHandle)
	}
	if opts.Identity.IsZero() {
		t.Error("no identity ref was wired, so the remote session id is never written back")
	}
}

// TestPoolSyncOptionsGivesEachPooledSessionItsOwnHandle pins settled decision
// 2 against the identity change. SessionDir is inherited by every session the
// pool mints, so a handle derived from the DIRECTORY would merge unrelated
// conversations into one durable transcript.
func TestPoolSyncOptionsGivesEachPooledSessionItsOwnHandle(t *testing.T) {
	wsRoot := t.TempDir()
	first := &chat.Session{SessionID: "principal-pool-1"}
	second := &chat.Session{SessionID: "principal-pool-2"}

	a := poolSyncOptions(first, first.SessionID, wsRoot, &config.Resolved{}, nil)
	b := poolSyncOptions(second, second.SessionID, wsRoot, &config.Resolved{}, nil)

	if a.LocalHandle == b.LocalHandle {
		t.Fatalf("two sessions sharing a wsRoot got one handle %q; their transcripts would merge", a.LocalHandle)
	}
	if a.OutboxDir == b.OutboxDir {
		t.Fatalf("two sessions sharing a wsRoot got one outbox %q", a.OutboxDir)
	}
	if again := poolSyncOptions(first, first.SessionID, wsRoot, &config.Resolved{}, nil); again.LocalHandle != a.LocalHandle {
		t.Errorf("handle is not stable across runs: %q then %q", a.LocalHandle, again.LocalHandle)
	}
}

// TestPoolSyncOptionsCarriesThePersistedWriterID is the TUI half of
// clichat.TestCLISyncOptionsCarriesThePersistedWriterID. Wiring one surface
// and not the other leaves the adopt/fork decision dead on the other.
func TestPoolSyncOptionsCarriesThePersistedWriterID(t *testing.T) {
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "principal-pool-writer"}

	first := poolSyncOptions(sess, sess.SessionID, wsRoot, &config.Resolved{}, nil)
	if first.ProjectorOptions.WriterID == "" {
		t.Fatal("WriterID is unset, so attach can never distinguish our own events from a foreign writer's")
	}

	stored, err := chatsync.LoadOrCreateIdentity(chatsync.IdentityDir(wsRoot), chatsync.IdentityKey(sess.SessionID))
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if first.ProjectorOptions.WriterID != stored.WriterID {
		t.Errorf("WriterID = %q, want the persisted %q", first.ProjectorOptions.WriterID, stored.WriterID)
	}
	if second := poolSyncOptions(sess, sess.SessionID, wsRoot, &config.Resolved{}, nil); second.ProjectorOptions.WriterID != first.ProjectorOptions.WriterID {
		t.Errorf("WriterID is not stable across runs: %q then %q; every restart would fork", first.ProjectorOptions.WriterID, second.ProjectorOptions.WriterID)
	}
}

// TestPoolSyncOptionsPersistsIdentityWithoutSessionDir is the TUI half of
// clichat.TestCLISyncOptionsPersistsIdentityWithoutSessionDir - the
// regression test for the resume-forks-a-new-remote-session bug. Every real
// pooled session gets SessionDir nulled by SetContextManager the instant
// context state is enabled, which every pooled session's context wiring
// does (see internal/uiadapter/session_pool.go's CreateFresh/GetOrCreate).
// poolSyncOptions must resolve identity from the caller-supplied wsRoot, not
// sess.SessionDir, or a resumed pooled session can never find its persisted
// RemoteSessionID.
func TestPoolSyncOptionsPersistsIdentityWithoutSessionDir(t *testing.T) {
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "principal-pool-resume"}
	if sess.SessionDir != "" {
		t.Fatalf("setup: SessionDir = %q, want empty - this test exists to prove the fix does not depend on it", sess.SessionDir)
	}
	res := &config.Resolved{}

	first := poolSyncOptions(sess, sess.SessionID, wsRoot, res, nil)
	if first.Identity.IsZero() {
		t.Fatal("no identity ref was wired even though wsRoot was supplied; identity would never persist")
	}

	const remoteID = "remote-session-after-attach"
	if err := chatsync.SaveIdentity(first.Identity.Dir, first.Identity.Key, chatsync.SyncIdentity{
		LocalHandle:     first.LocalHandle,
		RemoteSessionID: remoteID,
		WriterID:        first.ProjectorOptions.WriterID,
	}); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	// A resumed pool session: a brand-new *chat.Session, same SessionID, same
	// wsRoot, SessionDir still empty.
	resumed := &chat.Session{SessionID: sess.SessionID}
	second := poolSyncOptions(resumed, resumed.SessionID, wsRoot, res, nil)

	if second.LocalHandle != first.LocalHandle {
		t.Errorf("handle is not stable across a resumed process: %q then %q - every resume would fork a new remote session", first.LocalHandle, second.LocalHandle)
	}
	if second.RemoteSessionID != remoteID {
		t.Errorf("RemoteSessionID = %q, want %q from the prior run; AttachSession would create a new remote session instead of re-attaching", second.RemoteSessionID, remoteID)
	}
}
