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
	running   bool
	triggerCh chan struct{}
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewHeartbeatRunner creates a HeartbeatRunner with default status "waiting_input".
func NewHeartbeatRunner(client *Client, sessionID string, interval time.Duration) *HeartbeatRunner {
	return NewHeartbeatRunnerWithStatus(client, sessionID, interval, "waiting_input")
}

// NewHeartbeatRunnerWithStatus creates a HeartbeatRunner with the specified initial status.
func NewHeartbeatRunnerWithStatus(client *Client, sessionID string, interval time.Duration, status string) *HeartbeatRunner {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	if status == "" {
		status = "waiting_input"
	}
	return &HeartbeatRunner{
		client:    client,
		sessionID: sessionID,
		interval:  interval,
		status:    status,
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// SetSessionID updates the remote session ID for heartbeat requests.
func (h *HeartbeatRunner) SetSessionID(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionID = sessionID
}

// Start begins the heartbeat background goroutine.
func (h *HeartbeatRunner) Start(ctx context.Context) {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return
	}
	h.running = true
	h.mu.Unlock()

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
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	h.running = false
	close(h.stopCh)
	h.mu.Unlock()

	select {
	case <-h.doneCh:
	case <-ctx.Done():
	}
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
	sessID := h.sessionID
	h.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, _ = h.client.Heartbeat(reqCtx, sessID, status)
}
