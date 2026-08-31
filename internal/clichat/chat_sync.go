package clichat

import (
	"context"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// syncTokenProvider resolves the logged-in CLI session into the token
// provider chatsync requires. A nil result means there is nothing to
// authenticate with, and the caller must not start sync: uploading
// conversation content anonymously is the failure this returns nil to avoid.
func syncTokenProvider() chatsync.TokenProvider {
	svc, err := miviaauth.DefaultService()
	if err != nil {
		return nil
	}
	return chatsync.NewTokenProvider(svc)
}

// cliSyncOptions builds the SessionOptions the plain-CLI chat surface hands to
// chatsync.OpenSession. It is a separate function so a test can drive the
// exact value production uses, instead of asserting on a hand-built copy that
// can drift away from the wiring it claims to cover.
func cliSyncOptions(sess *chat.Session, res *config.Resolved, tokens chatsync.TokenProvider) chatsync.SessionOptions {
	return chatsync.SessionOptions{
		TokenProvider: tokens,
		ClientOptions: chatsync.ClientOptions{
			BaseURL: res.Sync.APIURL,
		},
		ProjectorOptions: chatsync.ProjectorOptions{
			IncludeToolIO:   res.Sync.IncludeToolIO,
			IncludeThinking: res.Sync.IncludeThinking,
		},
		OutboxDir:       filepath.Join(sess.SessionDir, "chat-sync", "sessions", sess.SessionID),
		MaxUnflushed:    res.Sync.MaxUnflushed,
		PollWaitSeconds: res.Sync.PollWaitSeconds,
		HeartbeatPeriod: config.SaturatingSeconds(res.Sync.HeartbeatSeconds),
		CreateTitle:     "CLI Session",
		EnablePolling:   false,
	}
}

func attachCLISync(sess *chat.Session, res *config.Resolved) func() {
	if res == nil || !res.Sync.Enabled || sess == nil || sess.EventBus == nil {
		return func() {}
	}
	tokens := syncTokenProvider()
	if tokens == nil {
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
