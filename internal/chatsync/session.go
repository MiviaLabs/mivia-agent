package chatsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

var syncKinds = []events.Kind{
	events.KindTurnStart,
	events.KindTurnEnd,
	events.KindError,
	events.KindAssistant,
	events.KindThinking,
	events.KindToolStart,
	events.KindToolEnd,
	events.KindSubagentStart,
	events.KindSubagentEnd,
	events.KindSubagentHeartbeat,
	events.KindSubagentDone,
	events.KindCompaction,
}

// SessionOptions configures a SyncSession.
type SessionOptions struct {
	ClientOptions    ClientOptions
	ProjectorOptions ProjectorOptions
	OutboxDir        string
	MaxUnflushed     int
	PollWaitSeconds  int
	HeartbeatPeriod  time.Duration
	CreateTitle      string
	CwdLabel         string
	HostLabel        string
	EnablePolling    bool
}

// SyncSession coordinates real-time synchronization between a local chat session,
// its outbox, and the remote API.
type SyncSession struct {
	localSessionID string
	sessionID      string
	client         *Client
	outbox         *Outbox
	projector      *Projector
	heartbeat      *HeartbeatRunner
	poller         *InputPoller
	sub            *events.Subscription
	opts           SessionOptions

	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	flushCh  chan struct{}
	forkedID string
}

// OpenSession opens or creates a remote session and begins synchronization.
func OpenSession(ctx context.Context, bus *events.Bus, sessionID string, opts SessionOptions) (*SyncSession, error) {
	client := NewClient(opts.ClientOptions)
	outbox, err := OpenOutbox(opts.OutboxDir, opts.MaxUnflushed)
	if err != nil {
		return nil, fmt.Errorf("open outbox: %w", err)
	}

	params := CreateSessionParams{
		Title:     opts.CreateTitle,
		CwdLabel:  opts.CwdLabel,
		HostLabel: opts.HostLabel,
	}

	att, err := AttachSession(ctx, client, outbox, params, sessionID, opts.ProjectorOptions.WriterID)
	if err != nil {
		_ = outbox.Close()
		return nil, fmt.Errorf("attach session: %w", err)
	}

	activeSessionID := att.SessionID
	localSessionID := sessionID
	if localSessionID == "" {
		localSessionID = activeSessionID
	}
	initialSeq := att.ServerSeq // Settled Decision S7: serverLastSeq is authoritative, never max(local, server)

	s := &SyncSession{
		localSessionID: localSessionID,
		sessionID:      activeSessionID,
		client:         client,
		outbox:         outbox,
		projector:      NewProjector(localSessionID, initialSeq, opts.ProjectorOptions),
		heartbeat:      NewHeartbeatRunner(client, activeSessionID, opts.HeartbeatPeriod),
		opts:           opts,
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		flushCh:        make(chan struct{}, 1),
		running:        true,
	}

	if opts.EnablePolling {
		s.poller = NewInputPoller(client, activeSessionID, opts.PollWaitSeconds)
	}

	s.sub = bus.SubscribeAcross(syncKinds, s, events.SubscribeOptions{BufSize: 1024})

	s.heartbeat.Start(ctx)
	if s.poller != nil {
		s.poller.Start(ctx)
	}

	go s.flushLoop(ctx)

	return s, nil
}

// HandleEvent processes incoming events in publish order across subscribed kinds.
func (s *SyncSession) HandleEvent(ctx context.Context, ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	drops := uint64(0)
	if s.sub != nil {
		drops = s.sub.Drops()
	}

	wireEvents := s.projector.ProjectWithDrops(ev, drops)
	if len(wireEvents) == 0 {
		return
	}

	if err := s.outbox.Append(wireEvents...); err != nil {
		return
	}

	s.triggerFlush()
}

func (s *SyncSession) triggerFlush() {
	select {
	case s.flushCh <- struct{}{}:
	default:
	}
}

// SessionID returns the currently active remote session ID.
func (s *SyncSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Inputs returns the channel of remote inputs.
func (s *SyncSession) Inputs() <-chan RemoteInput {
	if s.poller != nil {
		return s.poller.Inputs()
	}
	return nil
}

// SetStatus forwards status updates to the heartbeat runner.
func (s *SyncSession) SetStatus(ctx context.Context, status string) {
	if s.heartbeat != nil {
		s.heartbeat.SetStatus(ctx, status)
	}
}

// Stop terminates the session sync loop, flushes pending events, and closes resources.
func (s *SyncSession) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	s.mu.Unlock()

	if s.sub != nil {
		s.sub.Unsubscribe()
	}
	close(s.stopCh)
	<-s.doneCh

	if s.heartbeat != nil {
		s.heartbeat.Stop(ctx)
	}
	if s.poller != nil {
		s.poller.Stop()
	}

	_, _ = FlushOutbox(ctx, s.client, s.outbox, s.SessionID())
	return s.outbox.Close()
}

func (s *SyncSession) flushLoop(ctx context.Context) {
	defer close(s.doneCh)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			s.flushFinal(ctx)
			return
		case <-ctx.Done():
			s.flushFinal(ctx)
			return
		case <-ticker.C:
			s.flush(ctx)
		case <-s.flushCh:
			s.flush(ctx)
		}
	}
}

func (s *SyncSession) flush(ctx context.Context) {
	sessionID := s.SessionID()

	_, err := FlushOutbox(ctx, s.client, s.outbox, sessionID)
	if err != nil && errors.Is(err, ErrConflict) {
		s.handleFork(ctx)
	}
}

func (s *SyncSession) handleFork(ctx context.Context) {
	params := CreateSessionParams{
		Title:     s.opts.CreateTitle + " (forked)",
		CwdLabel:  s.opts.CwdLabel,
		HostLabel: s.opts.HostLabel,
	}
	created, err := s.client.CreateSession(ctx, params)
	if err != nil {
		return
	}

	s.mu.Lock()
	oldID := s.sessionID
	s.sessionID = created.ID
	s.forkedID = oldID
	newSessionID := s.sessionID

	if s.heartbeat != nil {
		s.heartbeat.SetSessionID(newSessionID)
	}
	if s.poller != nil {
		s.poller.SetSessionID(newSessionID)
	}

	forkPayload := &SyncForkedPayload{
		Envelope: Envelope{
			V:    1,
			At:   time.Now(),
			Turn: "synthetic:fork",
		},
		NewSessionID: newSessionID,
	}
	we := s.projector.nextWireEvent(TypeSyncForked, forkPayload)
	_ = s.outbox.Append(we)
	s.mu.Unlock()

	_, _ = FlushOutbox(ctx, s.client, s.outbox, newSessionID)
}

func (s *SyncSession) flushFinal(ctx context.Context) {
	s.mu.Lock()
	drops := uint64(0)
	if s.sub != nil {
		drops = s.sub.Drops()
	}
	wireEvents := s.projector.Flush(drops)
	if len(wireEvents) > 0 {
		_ = s.outbox.Append(wireEvents...)
	}
	s.mu.Unlock()

	s.flush(ctx)
}
