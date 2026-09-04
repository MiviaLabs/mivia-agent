package uiadapter

import (
	"context"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// WatcherConfig configures a RemoteInputWatcher.
type WatcherConfig struct {
	Seed           *chat.Session
	Tokens         chatsync.TokenProvider
	Res            *config.Resolved
	WorkspaceRoot  string
	AuthorProvider chatsync.AuthorUserIDProvider
	Max            int
	IsPooled       func(sessionID string) bool
	Deliver        func(sessionID string, in chatsync.RemoteInput)
}

// RemoteInputWatcher runs standalone chatsync.InputPoller instances for saved
// sessions that already have a persisted RemoteSessionID but are not currently
// pooled. It never opens an Outbox, never takes flock on events.jsonl, and
// never sends heartbeats or creates sessions.
type RemoteInputWatcher struct {
	mu       sync.Mutex
	watching map[string]*chatsync.InputPoller
	cfg      WatcherConfig
	stopped  bool
}

// NewRemoteInputWatcher constructs a watcher.
func NewRemoteInputWatcher(cfg WatcherConfig) *RemoteInputWatcher {
	if cfg.Max <= 0 {
		cfg.Max = 8
	}
	return &RemoteInputWatcher{
		watching: make(map[string]*chatsync.InputPoller),
		cfg:      cfg,
	}
}

// StopSync detaches and synchronously stops the poller for sessionID.
// It waits for the poller loop to exit so that no watcher writers remain on stateDir.
func (w *RemoteInputWatcher) StopSync(sessionID string, timeout time.Duration) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	poller, ok := w.watching[sessionID]
	if ok {
		delete(w.watching, sessionID)
	}
	w.mu.Unlock()
	if !ok || poller == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	poller.Stop(ctx)
	return nil
}

// Stop stops all watching pollers.
func (w *RemoteInputWatcher) Stop(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	pollers := make([]*chatsync.InputPoller, 0, len(w.watching))
	for _, p := range w.watching {
		if p != nil {
			pollers = append(pollers, p)
		}
	}
	w.watching = make(map[string]*chatsync.InputPoller)
	w.mu.Unlock()

	for _, p := range pollers {
		p.Stop(ctx)
	}
}

type candidateSession struct {
	sessionID string
	remoteID  string
	handle    chatsync.LocalHandle
	updatedAt time.Time
}

// candidates enumerates saved sessions that have a persisted RemoteSessionID.
func (w *RemoteInputWatcher) candidates() []candidateSession {
	if w == nil || w.cfg.Seed == nil {
		return nil
	}
	infos, err := w.cfg.Seed.ListSessions()
	if err != nil || len(infos) == 0 {
		return nil
	}
	anchor := chatSyncAnchor(w.cfg.WorkspaceRoot)
	identityDir := chatsync.IdentityDir(anchor)
	if identityDir == "" {
		return nil
	}

	var candidates []candidateSession
	for _, info := range infos {
		id := info.SessionID
		if id == "" {
			continue
		}
		if w.cfg.IsPooled != nil && w.cfg.IsPooled(id) {
			continue
		}
		key := chatsync.IdentityKey(id)
		ident, ok := chatsync.LoadIdentityReadOnly(identityDir, key)
		if !ok || ident.RemoteSessionID == "" {
			continue
		}
		candidates = append(candidates, candidateSession{
			sessionID: id,
			remoteID:  ident.RemoteSessionID,
			handle:    ident.LocalHandle,
			updatedAt: info.UpdatedAt,
		})
	}
	return candidates
}

// Backfill scans candidates and starts pollers for unpooled sessions up to cfg.Max.
func (w *RemoteInputWatcher) Backfill(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	slots := w.cfg.Max - len(w.watching)
	if slots <= 0 {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	candidates := w.candidates()
	if len(candidates) == 0 {
		return
	}

	if w.cfg.Tokens == nil {
		return
	}
	client, err := chatsync.NewClient(w.cfg.Tokens, chatsync.ClientOptions{
		BaseURL: chatsync.DefaultBaseURL(w.cfg.Res.Sync.APIURL),
	})
	if err != nil {
		return
	}

	anchor := chatSyncAnchor(w.cfg.WorkspaceRoot)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}

	for _, cand := range candidates {
		if len(w.watching) >= w.cfg.Max {
			break
		}
		if _, exists := w.watching[cand.sessionID]; exists {
			continue
		}
		if w.cfg.IsPooled != nil && w.cfg.IsPooled(cand.sessionID) {
			continue
		}

		outboxDir := chatsync.OutboxDirFor(anchor, cand.handle)
		poller := chatsync.NewInputPoller(
			client,
			cand.remoteID,
			w.cfg.Res.Sync.PollWaitSeconds,
			w.cfg.AuthorProvider,
			outboxDir,
		)
		w.watching[cand.sessionID] = poller
		poller.Start(ctx)

		go func(sessID string, p *chatsync.InputPoller) {
			for ri := range p.Inputs() {
				if w.cfg.Deliver != nil {
					w.cfg.Deliver(sessID, ri)
				}
			}
		}(cand.sessionID, poller)
	}
}
