package clichat

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// cliSyncOptions builds the SessionOptions the plain-CLI chat surface hands to
// chatsync.OpenSession. It is a separate function so a test can drive the
// exact value production uses, instead of asserting on a hand-built copy that
// can drift away from the wiring it claims to cover.
func cliSyncOptions(sess *chat.Session, res *config.Resolved, tokens chatsync.TokenProvider) chatsync.SessionOptions {
	// The sync identity is resolved HERE, before the options exist, because
	// chatsync.OpenSession opens the outbox before it attaches: OutboxDir must
	// already carry the local handle. A load error still yields a usable
	// identity - this run syncs under a handle the next run will not find,
	// which is strictly better than not syncing.
	identityDir := chatsync.IdentityDir(sess.SessionDir)
	key := chatsync.IdentityKey(sess.SessionID)
	ident, _ := chatsync.LoadOrCreateIdentity(identityDir, key)

	return chatsync.SessionOptions{
		TokenProvider: tokens,
		ClientOptions: chatsync.ClientOptions{
			BaseURL: chatsync.DefaultBaseURL(res.Sync.APIURL),
		},
		ProjectorOptions: chatsync.ProjectorOptions{
			IncludeToolIO:   res.Sync.IncludeToolIO,
			IncludeThinking: res.Sync.IncludeThinking,
			StreamAssistant: res.Sync.StreamAssistant,
			// chatsync is a leaf (settled decision 7), so these two are the
			// host's to supply. Both zero values are wrong rather than merely
			// absent: a nil ErrorMessage drops back to a default that does not
			// know this app's sentinels, and a false RedactToolArgs reads as
			// "the operator did not ask for redaction" when they may have.
			ErrorMessage: chat.TurnErrorMessage,
			// From the PERSISTED identity, never a per-run random: attach
			// compares this against the writer id on events the server holds
			// past our cursor, so a value that changed every run would read
			// our own previous run as foreign, end the remote session and
			// fork (REVIEW CHANGE 8's permanent data loss).
			WriterID:       ident.WriterID,
			RedactToolArgs: tools.RedactToolArgs(),
		},
		OutboxDir:       chatsync.OutboxDirFor(sess.SessionDir, ident.LocalHandle),
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		Identity:        chatsync.IdentityRef{Dir: identityDir, Key: key},
		MaxUnflushed:    res.Sync.MaxUnflushed,
		PollWaitSeconds: res.Sync.PollWaitSeconds,
		HeartbeatPeriod: config.SaturatingSeconds(res.Sync.HeartbeatSeconds),
		CreateTitle:     "CLI Session",
		EnablePolling:   false,
	}
}

// attachCLISync starts chat sync for the plain-CLI surface and returns the
// detach closure.
//
// Activation is authentication: a logged-in user syncs, a logged-out one does
// not, and neither state needs a flag or a prompt. `enabled = false` is the
// only way to say no while logged in - see config.ResolvedSync.Active. Every
// refusal here is silent by design: sync failing is never a reason to break
// the local chat the user actually asked for.
func attachCLISync(sess *chat.Session, res *config.Resolved) func() {
	if res == nil || sess == nil || sess.EventBus == nil {
		return func() {}
	}
	tokens := chatsync.DefaultTokenProvider()
	if !res.Sync.Active(tokens != nil) {
		return func() {}
	}
	opts := cliSyncOptions(sess, res, tokens)
	syncSess, err := chatsync.OpenSession(context.Background(), sess.EventBus, sess.SessionID, opts)
	if err != nil {
		return func() {}
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = syncSess.Stop(ctx)
	}
}
