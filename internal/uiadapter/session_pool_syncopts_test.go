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
	sess := &chat.Session{SessionID: "s1", SessionDir: t.TempDir()}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.SyncConfig{StreamAssistant: want}}
		got := poolSyncOptions(sess, "s1", res, nil).ProjectorOptions.StreamAssistant
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
	sess := &chat.Session{SessionID: principal, SessionDir: t.TempDir()}

	opts := poolSyncOptions(sess, principal, &config.Resolved{}, nil)

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
	dir := t.TempDir()
	first := &chat.Session{SessionID: "principal-pool-1", SessionDir: dir}
	second := &chat.Session{SessionID: "principal-pool-2", SessionDir: dir}

	a := poolSyncOptions(first, first.SessionID, &config.Resolved{}, nil)
	b := poolSyncOptions(second, second.SessionID, &config.Resolved{}, nil)

	if a.LocalHandle == b.LocalHandle {
		t.Fatalf("two sessions sharing a SessionDir got one handle %q; their transcripts would merge", a.LocalHandle)
	}
	if a.OutboxDir == b.OutboxDir {
		t.Fatalf("two sessions sharing a SessionDir got one outbox %q", a.OutboxDir)
	}
	if again := poolSyncOptions(first, first.SessionID, &config.Resolved{}, nil); again.LocalHandle != a.LocalHandle {
		t.Errorf("handle is not stable across runs: %q then %q", a.LocalHandle, again.LocalHandle)
	}
}

// TestPoolSyncOptionsCarriesThePersistedWriterID is the TUI half of
// clichat.TestCLISyncOptionsCarriesThePersistedWriterID. Wiring one surface
// and not the other leaves the adopt/fork decision dead on the other.
func TestPoolSyncOptionsCarriesThePersistedWriterID(t *testing.T) {
	sess := &chat.Session{SessionID: "principal-pool-writer", SessionDir: t.TempDir()}

	first := poolSyncOptions(sess, sess.SessionID, &config.Resolved{}, nil)
	if first.ProjectorOptions.WriterID == "" {
		t.Fatal("WriterID is unset, so attach can never distinguish our own events from a foreign writer's")
	}

	stored, err := chatsync.LoadOrCreateIdentity(chatsync.IdentityDir(sess.SessionDir), chatsync.IdentityKey(sess.SessionID))
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if first.ProjectorOptions.WriterID != stored.WriterID {
		t.Errorf("WriterID = %q, want the persisted %q", first.ProjectorOptions.WriterID, stored.WriterID)
	}
	if second := poolSyncOptions(sess, sess.SessionID, &config.Resolved{}, nil); second.ProjectorOptions.WriterID != first.ProjectorOptions.WriterID {
		t.Errorf("WriterID is not stable across runs: %q then %q; every restart would fork", first.ProjectorOptions.WriterID, second.ProjectorOptions.WriterID)
	}
}
