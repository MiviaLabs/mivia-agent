package clichat

import (
	"context"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func attachCLISync(sess *chat.Session, res *config.Resolved) func() {
	if res == nil || !res.Sync.Enabled || sess == nil || sess.EventBus == nil {
		return func() {}
	}
	opts := chatsync.SessionOptions{
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
