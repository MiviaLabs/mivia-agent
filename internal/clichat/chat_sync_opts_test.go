package clichat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestCLISyncOptionsCarriesStreamAssistant pins that sync.stream_assistant
// reaches the projector on the plain-CLI surface too. Wiring one surface and
// not the other is how a config key becomes surface-dependent with no error.
func TestCLISyncOptionsCarriesStreamAssistant(t *testing.T) {
	sess := &chat.Session{SessionID: "s1", SessionDir: t.TempDir()}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.SyncConfig{StreamAssistant: want}}
		got := cliSyncOptions(sess, res, nil).ProjectorOptions.StreamAssistant
		if got != want {
			t.Errorf("StreamAssistant = %v, want %v", got, want)
		}
	}
}

// TestCLISyncOptionsKeepsThePrincipalOutOfTheOutboxPath pins the identity
// separation at the wiring site. chat.Session.SessionID is the contextstate
// authorization subject; naming a durable directory after it writes a live
// capability into the filesystem layout, and it was previously the outbox
// directory name verbatim.
func TestCLISyncOptionsKeepsThePrincipalOutOfTheOutboxPath(t *testing.T) {
	const principal = "PRINCIPALCLIAAAAAAAAAAAAAA"
	sess := &chat.Session{SessionID: principal, SessionDir: t.TempDir()}
	res := &config.Resolved{}

	opts := cliSyncOptions(sess, res, nil)

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
	if strings.Contains(opts.Identity.Key, principal) {
		t.Errorf("the identity key %q embeds the principal", opts.Identity.Key)
	}
}

// TestCLISyncOptionsReusesThePersistedHandle is the cross-run half: a second
// call with the same session must resolve the SAME outbox, or every restart
// starts a new transcript and the surviving cursor is orphaned.
func TestCLISyncOptionsReusesThePersistedHandle(t *testing.T) {
	sess := &chat.Session{SessionID: "principal-cli-stable", SessionDir: t.TempDir()}
	res := &config.Resolved{}

	first := cliSyncOptions(sess, res, nil)
	second := cliSyncOptions(sess, res, nil)

	if first.LocalHandle != second.LocalHandle {
		t.Errorf("handle is not stable across runs: %q then %q", first.LocalHandle, second.LocalHandle)
	}
	if first.OutboxDir != second.OutboxDir {
		t.Errorf("outbox dir is not stable across runs: %q then %q", first.OutboxDir, second.OutboxDir)
	}
}

// TestCLISyncOptionsCarriesThePersistedWriterID is the DC-27 case for
// ProjectorOptions.WriterID: the field existed, attach.go's adopt/fork branch
// read it, and NO production site set it, so the branch never ran and the
// package's own tests made the mechanism look alive.
//
// It must come from the PERSISTED identity. A per-run random is worse than
// leaving it unset: every restart would read its own previous run's events as
// foreign, end the remote session and fork - the permanent data loss REVIEW
// CHANGE 8 exists to prevent.
func TestCLISyncOptionsCarriesThePersistedWriterID(t *testing.T) {
	sess := &chat.Session{SessionID: "principal-cli-writer", SessionDir: t.TempDir()}
	res := &config.Resolved{}

	first := cliSyncOptions(sess, res, nil)
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
	if second := cliSyncOptions(sess, res, nil); second.ProjectorOptions.WriterID != first.ProjectorOptions.WriterID {
		t.Errorf("WriterID is not stable across runs: %q then %q; every restart would fork", first.ProjectorOptions.WriterID, second.ProjectorOptions.WriterID)
	}
}
