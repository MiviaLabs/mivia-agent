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
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "s1"}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.ResolvedSync{StreamAssistant: want}}
		got := cliSyncOptions(sess, wsRoot, res, nil).ProjectorOptions.StreamAssistant
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
	sess := &chat.Session{SessionID: principal}
	res := &config.Resolved{}

	opts := cliSyncOptions(sess, t.TempDir(), res, nil)

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
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "principal-cli-stable"}
	res := &config.Resolved{}

	first := cliSyncOptions(sess, wsRoot, res, nil)
	second := cliSyncOptions(sess, wsRoot, res, nil)

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
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "principal-cli-writer"}
	res := &config.Resolved{}

	first := cliSyncOptions(sess, wsRoot, res, nil)
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
	if second := cliSyncOptions(sess, wsRoot, res, nil); second.ProjectorOptions.WriterID != first.ProjectorOptions.WriterID {
		t.Errorf("WriterID is not stable across runs: %q then %q; every restart would fork", first.ProjectorOptions.WriterID, second.ProjectorOptions.WriterID)
	}
}

// TestCLISyncOptionsPersistsIdentityWithoutSessionDir is the regression test
// for the resume-forks-a-new-remote-session bug: every real chat.Session gets
// SessionDir nulled by SetContextManager the instant context state is
// enabled (internal/chat/context_integration.go), which happens on every
// production `mivia chat` invocation - so SessionDir is always "" in
// practice. Before this fix, cliSyncOptions read sess.SessionDir directly,
// which meant chatsync.IdentityDir always resolved to "", LoadOrCreateIdentity
// always minted a fresh unpersisted identity, and AttachSession could never
// find a RemoteSessionID to re-attach to. This pins that identity now
// persists and is found on a second call using ONLY wsRoot, with
// sess.SessionDir left at its real-world zero value throughout.
func TestCLISyncOptionsPersistsIdentityWithoutSessionDir(t *testing.T) {
	wsRoot := t.TempDir()
	sess := &chat.Session{SessionID: "principal-resume-no-sessiondir"}
	if sess.SessionDir != "" {
		t.Fatalf("setup: SessionDir = %q, want empty - this test exists to prove the fix does not depend on it", sess.SessionDir)
	}
	res := &config.Resolved{}

	first := cliSyncOptions(sess, wsRoot, res, nil)
	if first.Identity.IsZero() {
		t.Fatal("no identity ref was wired even though wsRoot was supplied; identity would never persist")
	}

	// Simulate a completed attach: the real OpenSession would call
	// SessionOptions.persistRemoteSessionID after AttachSession succeeds
	// (internal/chatsync/session.go). Do that write directly here so this
	// test stays a pure unit test of cliSyncOptions, not a full OpenSession
	// integration test.
	const remoteID = "remote-session-after-attach"
	if err := chatsync.SaveIdentity(first.Identity.Dir, first.Identity.Key, chatsync.SyncIdentity{
		LocalHandle:     first.LocalHandle,
		RemoteSessionID: remoteID,
		WriterID:        first.ProjectorOptions.WriterID,
	}); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	// A resumed process: a brand-new *chat.Session (as every real `mivia
	// chat --session <name>` invocation mints), same SessionID (Load already
	// retargets it on resume - see internal/chat/resume_reclaim_test.go),
	// same wsRoot, SessionDir still empty.
	resumed := &chat.Session{SessionID: sess.SessionID}
	second := cliSyncOptions(resumed, wsRoot, res, nil)

	if second.LocalHandle != first.LocalHandle {
		t.Errorf("handle is not stable across a resumed process: %q then %q - every resume would fork a new remote session", first.LocalHandle, second.LocalHandle)
	}
	if second.RemoteSessionID != remoteID {
		t.Errorf("RemoteSessionID = %q, want %q from the prior run; AttachSession would create a new remote session instead of re-attaching", second.RemoteSessionID, remoteID)
	}
}
