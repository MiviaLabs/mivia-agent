package chatsync

import (
	"context"
	"sync"
	"time"
)

const (
	DefaultHeartbeatInterval = 30 * time.Second
)

// HeartbeatRunner maintains the periodic and event-driven heartbeat for an active session.
type HeartbeatRunner struct {
	client    *Client
	sessionID string
	interval  time.Duration
	status    string
	mu        sync.Mutex
	triggerCh chan struct{}
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewHeartbeatRunner creates a HeartbeatRunner.
func NewHeartbeatRunner(client *Client, sessionID string, interval time.Duration) *HeartbeatRunner {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	return &HeartbeatRunner{
		client:    client,
		sessionID: sessionID,
		interval:  interval,
		status:    "running",
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Start begins the heartbeat background goroutine.
func (h *HeartbeatRunner) Start(ctx context.Context) {
	go h.loop(ctx)
}

// SetStatus updates the current status and triggers an immediate heartbeat send.
func (h *HeartbeatRunner) SetStatus(ctx context.Context, status string) {
	h.mu.Lock()
	if h.status == status {
		h.mu.Unlock()
		return
	}
	h.status = status
	h.mu.Unlock()

	select {
	case h.triggerCh <- struct{}{}:
	default:
	}
}

// Stop terminates the heartbeat runner.
func (h *HeartbeatRunner) Stop(ctx context.Context) {
	close(h.stopCh)
	<-h.doneCh
}

func (h *HeartbeatRunner) loop(ctx context.Context) {
	defer close(h.doneCh)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	h.send(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.send(ctx)
		case <-h.triggerCh:
			h.send(ctx)
		}
	}
}

func (h *HeartbeatRunner) send(ctx context.Context) {
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, _ = h.client.Heartbeat(reqCtx, h.sessionID, status)
}
