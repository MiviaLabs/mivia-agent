package chatsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	// TokenProvider is required. OpenSession refuses to start without it.
	TokenProvider    TokenProvider
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

// outboxAppender is the durable-append half of the outbox contract.
type outboxAppender interface {
	Append(events ...WireEvent) error
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

	// appender is the outbox write seam. Production always holds the real
	// *Outbox. Tests substitute a failing or stalling appender to reach states
	// a real file cannot be driven into on demand - a mid-batch write or fsync
	// failure that is NOT an overflow, and a disk slow enough to prove the bus
	// handler does not wait on it.
	appender outboxAppender

	// dropSource reports the total number of events lost before projection.
	// It is a seam so a test can drive the sync.dropped path deterministically
	// instead of racing the bus into shedding events.
	dropSource func() uint64

	// remoteEnded latches when the server reports the remote session is gone
	// (409). Settled decision: a flush 409 stops the pusher, the poller and the
	// heartbeat; it never forks. Local chat is untouched.
	remoteEnded atomic.Bool

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	eventCh chan events.Event
	flushCh chan struct{}
}

// OpenSession opens or creates a remote session and begins synchronization.
func OpenSession(ctx context.Context, bus *events.Bus, sessionID string, opts SessionOptions) (*SyncSession, error) {
	client, err := NewClient(opts.TokenProvider, opts.ClientOptions)
	if err != nil {
		return nil, err
	}
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
	initialSeq := att.ServerSeq
	if maxSeq := outbox.MaxSeq(); maxSeq > initialSeq {
		initialSeq = maxSeq
	}

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
		eventCh:        make(chan events.Event, 1024),
		flushCh:        make(chan struct{}, 1),
		running:        true,
	}
	s.appender = outbox

	if att.ForkedFrom != "" {
		if err := s.applyForkedAttach(); err != nil {
			_ = outbox.Close()
			return nil, fmt.Errorf("apply forked attach: %w", err)
		}
	}

	if opts.EnablePolling {
		s.poller = NewInputPoller(client, activeSessionID, opts.PollWaitSeconds, opts.OutboxDir)
	}

	s.sub = bus.SubscribeAcross(syncKinds, s, events.SubscribeOptions{BufSize: 1024})
	s.dropSource = s.busDrops

	s.heartbeat.Start(ctx)
	if s.poller != nil {
		s.poller.Start(ctx)
	}

	go s.workerLoop(ctx)

	return s, nil
}

// HandleEvent processes incoming events via non-blocking send to an internal channel.
func (s *SyncSession) HandleEvent(ctx context.Context, ev events.Event) {
	if s.remoteEnded.Load() {
		return
	}

	s.mu.Lock()
	running := s.running
	s.mu.Unlock()

	if !running {
		return
	}

	select {
	case s.eventCh <- ev:
	default:
	}
}

func (s *SyncSession) processEvent(ctx context.Context, ev events.Event) {
	s.mu.Lock()
	wireEvents := s.projector.ProjectWithDrops(ev, s.currentDrops())
	if len(wireEvents) == 0 {
		s.mu.Unlock()
		return
	}

	if err := s.appendLocked(wireEvents); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	s.triggerFlush()
}

// appendLocked durably appends wireEvents and rolls the seq counter back on
// ANY failure, not only on overflow.
//
// A seq the projector consumed but the outbox never stored leaves a permanent
// hole in the stream. The server's contiguity check then rejects every later
// append with a 400 for the rest of the process lifetime, so a single transient
// write or fsync error wedges sync for good. The counter must therefore track
// what was STORED, never what was merely assigned.
//
// The caller must hold s.mu.
func (s *SyncSession) appendLocked(wireEvents []WireEvent) error {
	if err := s.appender.Append(wireEvents...); err != nil {
		s.projector.RollbackSeq(len(wireEvents))
		return err
	}
	return nil
}

// currentDrops reports every event lost before projection. The caller must
// hold s.mu.
func (s *SyncSession) currentDrops() uint64 {
	if s.dropSource == nil {
		return 0
	}
	return s.dropSource()
}

func (s *SyncSession) busDrops() uint64 {
	if s.sub == nil {
		return 0
	}
	return s.sub.Drops()
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

// LastSeq returns the current sequence number assigned by the projector.
func (s *SyncSession) LastSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projector.LastSeq()
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

	if !s.remoteEnded.Load() {
		_, _ = FlushOutbox(ctx, s.client, s.outbox, s.SessionID())
	}
	return s.outbox.Close()
}

func (s *SyncSession) workerLoop(ctx context.Context) {
	defer close(s.doneCh)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// A 409 ended the remote session. There is nothing left to push, so
		// exit without a final flush rather than replay into a dead session.
		if s.remoteEnded.Load() {
			return
		}
		select {
		case <-s.stopCh:
			s.drainAndFlushFinal(ctx)
			return
		case <-ctx.Done():
			s.drainAndFlushFinal(ctx)
			return
		case ev := <-s.eventCh:
			s.processEvent(ctx, ev)
		case <-s.flushCh:
			s.flush(ctx)
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

func (s *SyncSession) flush(ctx context.Context) {
	if s.remoteEnded.Load() {
		return
	}
	sessionID := s.SessionID()

	_, err := FlushOutbox(ctx, s.client, s.outbox, sessionID)
	if err == nil {
		return
	}
	// ErrConflict: the server ended this session. ErrAuthStop: the settled
	// 401 policy - ErrReauthRequired / ErrSessionLost cannot be recovered
	// without `mivia login`, which this path must never prompt for. Both are
	// terminal for sync and neither touches the local chat.
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrAuthStop) {
		s.handleRemoteEnd(ctx)
	}
}

// handleRemoteEnd latches the remote-ended state and shuts the pusher, the
// poller and the heartbeat down. It never creates a replacement session: a 409
// on append means the server ended this session, which is terminal for sync.
// The local chat is untouched.
//
// It is called from the worker goroutine, so the (blocking) runner stops are
// detached. Both runner Stop methods are idempotent, so a later Stop(ctx) that
// races this one is safe.
func (s *SyncSession) handleRemoteEnd(ctx context.Context) {
	if !s.remoteEnded.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if s.heartbeat != nil {
			s.heartbeat.Stop(ctx)
		}
		if s.poller != nil {
			s.poller.Stop()
		}
	}()
}

// applyForkedAttach re-bases the outbox onto a session that AttachSession
// forked because a foreign writer owned the old one, and records a
// sync.forked marker so a viewer can follow the new session.
func (s *SyncSession) applyForkedAttach() error {
	unflushedCount, err := s.outbox.ResetForFork()
	if err != nil {
		return fmt.Errorf("reset outbox for fork: %w", err)
	}
	s.projector.ResetSeq(int64(unflushedCount))

	we := s.projector.nextWireEvent(TypeSyncForked, &SyncForkedPayload{
		Envelope: Envelope{
			V:    1,
			At:   time.Now(),
			Turn: "synthetic:fork",
		},
		NewSessionID: s.sessionID,
	})
	if err := s.appendLocked([]WireEvent{we}); err != nil {
		return fmt.Errorf("append fork marker: %w", err)
	}
	return nil
}

func (s *SyncSession) drainAndFlushFinal(ctx context.Context) {
	for {
		select {
		case ev := <-s.eventCh:
			s.processEvent(ctx, ev)
		default:
			goto drained
		}
	}
drained:
	s.mu.Lock()
	wireEvents := s.projector.Flush(s.currentDrops())
	if len(wireEvents) > 0 {
		_ = s.appendLocked(wireEvents)
	}
	s.mu.Unlock()

	s.flush(ctx)
}
