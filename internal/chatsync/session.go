package chatsync

import (
	"context"
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
	// RemoteSessionID is the server-assigned session to re-attach to, read
	// from the persisted identity. Empty creates a fresh remote session. It is
	// the ONLY value that may appear in a request URL, and it is deliberately
	// NOT the chat session id OpenSession filters the bus on.
	RemoteSessionID string
	// LocalHandle is the local-only handle that names OutboxDir. It is carried
	// here so the identity write-back can rewrite the record without re-reading
	// (and possibly re-minting) it.
	LocalHandle LocalHandle
	// Identity names the file the resolved remote session id is written back
	// to. Zero disables the write-back: the session then works exactly as
	// before, but the next run cannot find the remote session it created.
	Identity        IdentityRef
	MaxUnflushed    int
	PollWaitSeconds int
	HeartbeatPeriod time.Duration
	CreateTitle     string
	CwdLabel        string
	HostLabel       string
	EnablePolling   bool
	// EventBufSize bounds the handler-to-worker channel. Zero means
	// defaultEventBufSize. Overflow here is real loss and is counted into the
	// sync.dropped marker, exactly like loss on the bus.
	EventBufSize int
}

// defaultEventBufSize is the handler-to-worker channel depth.
const defaultEventBufSize = 1024

func eventBufSize(n int) int {
	if n <= 0 {
		return defaultEventBufSize
	}
	return n
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

	// unsubscribe releases the bus subscription. It is a field, not a direct
	// s.sub.Unsubscribe call, so a test can observe WHEN it runs relative to
	// the final Drops() read.
	unsubscribe func()

	// dropSource reports the total number of events lost before projection.
	// It is a seam so a test can drive the sync.dropped path deterministically
	// instead of racing the bus into shedding events.
	dropSource func() uint64

	// remoteEnded latches when the server reports the remote session is gone
	// (409). Settled decision: a flush 409 stops the pusher, the poller and the
	// heartbeat; it never forks. Local chat is untouched.
	remoteEnded atomic.Bool

	// stopReason records WHY sync stopped. A terminal stop that says nothing
	// is indistinguishable from a healthy but idle session, which is the
	// dishonest-status half of the settled poison rule
	// (chat-sync-event-contract.md:285-287: stop syncing and SAY SO).
	stopReason atomic.Pointer[string]

	// stopCtxCh hands Stop's context to the worker so the final drain and
	// flush is bounded by the caller's shutdown deadline rather than by the
	// unbounded session context.
	stopCtxCh chan context.Context

	// chanDrops counts events HandleEvent could not enqueue because the
	// worker channel was full. This is a SECOND loss hop, downstream of the
	// bus's own drop-oldest queue. Left uncounted it produced a contiguous,
	// complete-LOOKING transcript that was silently missing content - the
	// exact failure settled decision 6 exists to make visible.
	chanDrops atomic.Uint64

	// running is atomic, not guarded by mu: HandleEvent runs on the bus
	// delivery goroutine and must never contend for a lock the outbox writer
	// holds across an fsync.
	running atomic.Bool

	// retryBase and retryAt hold the jittered push-retry schedule. They are
	// touched only by the worker goroutine, so they need no lock.
	retryBase time.Duration
	retryAt   time.Time

	// lastGapBase is the server mark the last sequence-gap rebase used, or
	// noGapBase when none is outstanding. A second gap at the same mark proves
	// the rebase moved nothing, which is the loop guard on the rebase path.
	// Worker-goroutine only, like the retry schedule.
	lastGapBase int64

	mu      sync.Mutex
	stopCh  chan struct{}
	doneCh  chan struct{}
	eventCh chan events.Event
	flushCh chan struct{}
}

// OpenSession opens or creates a remote session and begins synchronization.
//
// chatSessionID is the LOCAL chat session id and is used for exactly one
// thing: filtering the event bus, which stamps that id on every event. It is
// the contextstate authorization subject, so it never reaches a URL, a request
// body or a file. The session to attach to comes from opts.RemoteSessionID and
// the outbox directory from opts.OutboxDir, both resolved by the caller from
// the persisted identity - the caller must resolve them, because the outbox is
// opened here BEFORE the attach, so OutboxDir has to already carry the handle.
func OpenSession(ctx context.Context, bus *events.Bus, chatSessionID string, opts SessionOptions) (*SyncSession, error) {
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

	att, err := AttachSession(ctx, client, outbox, params, opts.RemoteSessionID, opts.ProjectorOptions.WriterID)
	if err != nil {
		_ = outbox.Close()
		return nil, fmt.Errorf("attach session: %w", err)
	}

	activeSessionID := att.SessionID
	// A write-back failure costs the NEXT run its resume, not this one: this
	// session is attached and healthy. Failing here would trade a degraded
	// restart for no sync at all.
	_ = opts.persistRemoteSessionID(activeSessionID)
	localSessionID := chatSessionID
	initialSeq, err := openingSeq(outbox, att)
	if err != nil {
		_ = outbox.Close()
		return nil, err
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
		eventCh:        make(chan events.Event, eventBufSize(opts.EventBufSize)),
		flushCh:        make(chan struct{}, 1),
		stopCtxCh:      make(chan context.Context, 1),
		lastGapBase:    noGapBase,
	}
	s.running.Store(true)
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
	s.dropSource = s.preProjectionDrops
	s.unsubscribe = s.sub.Unsubscribe

	s.heartbeat.Start(ctx)
	if s.poller != nil {
		s.poller.Start(ctx)
	}

	go s.workerLoop(ctx)

	return s, nil
}

// openingSeq resolves the seq the projector starts numbering from.
//
// serverLastSeq is authoritative, never max(local, server)
// (chat-sync-cli-slice.md:86, :253). Assigned-but-unsent seqs do not survive a
// restart as numbers - only as CONTENT - because the counter is re-derived
// from the server (REVIEW CHANGE 4). Keeping the local high-water mark opens
// the stream past the server's next expected seq, which is the sequence-gap
// 400: the runtime rebase then recovers the outbox onto the server's mark
// while the projector keeps its stale counter, so the very next event gaps
// again at the SAME mark - and a second gap at one mark is what the rebase
// loop guard reads as "the rebase moved nothing" and poisons sync on.
//
// The unflushed events are not discarded. They are renumbered onto the server
// mark, which is the same operation a fork does with base 0.
func openingSeq(outbox *Outbox, att *SessionAttachment) (int64, error) {
	if outbox.MaxSeq() <= att.ServerSeq {
		return att.ServerSeq, nil
	}
	// A fork rebases onto 0 in applyForkedAttach and must not be rebased twice.
	if att.ForkedFrom != "" {
		return att.ServerSeq, nil
	}
	n, err := outbox.Rebase(att.ServerSeq)
	if err != nil {
		return 0, fmt.Errorf("rebase outbox onto the server mark: %w", err)
	}
	return att.ServerSeq + int64(n), nil
}

// HandleEvent processes incoming events via non-blocking send to an internal channel.
func (s *SyncSession) HandleEvent(ctx context.Context, ev events.Event) {
	if s.remoteEnded.Load() {
		return
	}

	if !s.running.Load() {
		return
	}

	select {
	case s.eventCh <- ev:
	default:
		s.chanDrops.Add(1)
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
		s.projector.RollbackDrops(droppedDelta(wireEvents))
		return err
	}
	return nil
}

// droppedDelta sums the loss reported by the sync.dropped markers in a batch.
// A batch carries at most one, but summing keeps the helper correct rather than
// resting on that.
func droppedDelta(wireEvents []WireEvent) uint64 {
	var total uint64
	for _, we := range wireEvents {
		if we.Type != TypeSyncDropped {
			continue
		}
		if p, ok := we.Payload.(*SyncDroppedPayload); ok {
			total += p.Dropped
		}
	}
	return total
}

// currentDrops reports every event lost before projection. The caller must
// hold s.mu.
func (s *SyncSession) currentDrops() uint64 {
	if s.dropSource == nil {
		return 0
	}
	return s.dropSource()
}

// preProjectionDrops is the total loss before projection: the bus's own
// drop-oldest shedding plus this session's handler-to-worker channel. Both
// counters are monotonic, so their sum is monotonic and the projector's
// advance check stays correct.
func (s *SyncSession) preProjectionDrops() uint64 {
	var busDropped uint64
	if s.sub != nil {
		busDropped = s.sub.Drops()
	}
	return busDropped + s.chanDrops.Load()
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

// Stopped reports whether sync has latched a terminal stop: a 409, a fatal
// auth failure, or a poison 400. The local chat is unaffected either way.
func (s *SyncSession) Stopped() bool { return s.remoteEnded.Load() }

// StopReason returns why sync stopped, or the empty string while it runs.
func (s *SyncSession) StopReason() string {
	if v := s.stopReason.Load(); v != nil {
		return *v
	}
	return ""
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

// Stop terminates the session sync loop, flushes pending events, and closes
// resources. Every wait it performs is bounded by ctx: a caller that gives Stop
// a 200ms deadline gets control back inside 200ms even when a long poll is
// parked and the final append is stalled on a dead network.
//
// When the deadline arrives first, Stop returns ctx.Err() and hands the outbox
// close to a goroutine that waits for the worker, because closing the outbox
// under a worker still writing to it would race its file handle.
func (s *SyncSession) Stop(ctx context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return nil
	}

	// The worker's final drain and flush inherits the caller's deadline.
	select {
	case s.stopCtxCh <- ctx:
	default:
	}
	close(s.stopCh)

	timedOut := false
	select {
	case <-s.doneCh:
	case <-ctx.Done():
		timedOut = true
	}

	// Unsubscribe only AFTER the worker read Drops() for the last time.
	// events.Subscription documents that reading the counter after
	// Unsubscribe can over-report: Publish snapshots the subscriber slice
	// under the lock and enqueues outside it, so a publish already in flight
	// still lands in the removed subscription's queue and is dropped there.
	// Nothing was lost - no handler was ever going to run for it - but the
	// counter moves. Reading it in that window makes the final flush record a
	// sync.dropped marker for a hole that does not exist, and settled decision
	// 6 makes that marker PERMANENT in the transcript.
	//
	// On the TIMEOUT path the worker is still running, so releasing here would
	// reopen exactly that window: the worker's drainAndFlushFinal has not yet
	// made its last Drops() read. The release therefore moves into the same
	// goroutine that already waits for the worker before closing the outbox.
	if !timedOut && s.unsubscribe != nil {
		s.unsubscribe()
	}

	if s.heartbeat != nil {
		s.heartbeat.Stop(ctx)
	}
	if s.poller != nil {
		s.poller.Stop(ctx)
	}

	if timedOut {
		go func() {
			<-s.doneCh
			if s.unsubscribe != nil {
				s.unsubscribe()
			}
			_ = s.outbox.Close()
		}()
		return ctx.Err()
	}

	if !s.remoteEnded.Load() {
		_, _ = FlushOutbox(ctx, s.client, s.outbox, s.SessionID())
	}
	return s.outbox.Close()
}

// shutdownCtx returns the deadline Stop asked the worker to shut down under,
// falling back to the session context when the worker is unwinding for another
// reason (a cancelled session context).
func (s *SyncSession) shutdownCtx(base context.Context) context.Context {
	select {
	case c := <-s.stopCtxCh:
		if c != nil {
			return c
		}
	default:
	}
	return base
}
