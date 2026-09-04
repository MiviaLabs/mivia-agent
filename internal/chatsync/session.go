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
	events.KindAssistantReset,
	events.KindThinking,
	events.KindToolStart,
	events.KindToolEnd,
	events.KindSubagentBegin,
	events.KindSubagentStart,
	events.KindSubagentEnd,
	events.KindSubagentHeartbeat,
	events.KindSubagentDone,
	events.KindCompaction,
	// Hook runs are part of the transcript a remote viewer reads: the
	// projector has a wire type, a contract row and a metrics entry for
	// them, but without the kind here the SUBSCRIPTION - the only
	// production feed into the projector - never delivered one, and every
	// hook.ran row stayed dead code. A kind the wire advertises but the
	// feed drops is exactly the gap the viewer-surfaces gate exists for.
	events.KindHook,
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
	Telemetry       *SyncTelemetry
	CreateTitle     string
	CwdLabel        string
	HostLabel       string
	EnablePolling   bool
	// AuthorUserIDProvider resolves the CLI's own authenticated principal id
	// for verifying who queued a remote input. Required for EnablePolling to
	// deliver anything: nil verifies nothing, so InputPoller refuses every
	// input closed. See chatsync.AuthorUserIDProvider.
	AuthorUserIDProvider AuthorUserIDProvider
	// OnInputRejected, when set, is called for every remote input
	// InputPoller refuses (session id mismatch, unsupported kind, malformed
	// body, unverifiable or mismatched author). Nil means refusals stay
	// silent to the host.
	OnInputRejected func(id, sessionID, reason string)
	// EventBufSize bounds the handler-to-worker channel. Zero means
	// defaultEventBufSize. Overflow here is real loss and is counted into the
	// sync.dropped marker, exactly like loss on the bus.
	EventBufSize int

	// OnStop is called exactly once, with the same string StopReason returns,
	// when sync latches a terminal stop: a fatal auth failure, a poison 400,
	// or a recovery bound. It is never called for an orderly Stop, and never
	// for a 409 or 404 on their own - those recover onto a new session.
	//
	// It exists because the contract's poison rule is "stop syncing and SAY
	// SO", and a reason only a getter can reach says nothing: no host polls
	// SyncSession, so a silent stop looks exactly like a healthy idle
	// session. Nil is valid and means the host does not surface stops.
	//
	// It runs on a detached goroutine, NOT on the worker: the worker still has
	// a drain and a final flush to finish, and a host callback that blocks
	// (writing to a terminal, or sending on a full UI channel) must not be
	// able to hold that up.
	OnStop func(reason string)

	// OnDegraded is called once when pushes stop landing - three consecutive
	// failures, or a failure a minute after the last success - and
	// OnRecovered once when they land again. Neither is a stop: the outbox
	// keeps the backlog and the retry schedule keeps trying. They exist
	// because a transient failure that never ends is otherwise silent for the
	// whole session (flushNow's default branch retries forever and says
	// nothing), and the same fact is written to status.json in OutboxDir so
	// it outlives the process. Both run on a detached goroutine, like OnStop.
	OnDegraded  func(reason string)
	OnRecovered func()
}

// defaultEventBufSize is the handler-to-worker channel depth.
const defaultEventBufSize = 1024

// RecommendedStopTimeout is the ctx budget a caller should give Stop for its
// FINAL flush - one real network round trip carrying whatever backlog is
// still unflushed, in a single request (FlushOutbox sends the whole
// backlog as one batch, uncapped). 2 seconds - the previous ad-hoc value at
// both call sites - is not that budget: it is sized for a small, mostly-
// idle turn's tail, and a real remote server plus a genuinely accumulated
// backlog (periodic mid-turn flushes cannot always keep pace with local
// event generation, especially once [sync].stream_assistant multiplies
// event count 5-10x) can legitimately take several seconds. A ctx that
// expires first makes Stop return early via its timedOut path, which hands
// the outbox close to a background goroutine the caller's own process exit
// then kills - so the accumulated backlog is not merely delayed, it is
// permanently lost: nothing re-opens a one-shot run's outbox directory on a
// later invocation. 15 seconds is a real chance at that one request
// without leaving process exit hanging indefinitely on a truly dead
// network - Stop still returns ctx.Err() if that budget runs out too.
const RecommendedStopTimeout = 15 * time.Second

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

	// health tracks push health for OnDegraded/OnRecovered and status.json.
	// It has its own lock: the uploader records pushes and Stop records the
	// end, off the uploader.
	health *syncHealth

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

	// remoteEnded latches a terminal stop: a fatal auth failure, a poison
	// 400, or a recovery bound (see classifyFlushError and
	// recoverRemoteSession). It stops the pusher, the poller and the
	// heartbeat. Local chat is untouched.
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

	// appendDrops counts events whose projection was built but never stored,
	// because the durable append failed - overflow of the bounded outbox
	// (ErrOutboxOverflow) above all, which is what a slow or offline uplink
	// reaches first.
	//
	// This is a THIRD loss hop, and until it was counted it was the only
	// silent one: the two upstream hops shed events into chanDrops and the
	// bus counter, which surface as a sync.dropped marker, while this one
	// rolled the seq back and returned, leaving a contiguous, complete-LOOKING
	// transcript with content missing from the middle. That is precisely the
	// failure settled decision 6 exists to make visible, so this counter feeds
	// the same marker as the other two.
	appendDrops atomic.Uint64

	// running is atomic, not guarded by mu: HandleEvent runs on the bus
	// delivery goroutine and must never contend for a lock the outbox writer
	// holds across an fsync.
	running atomic.Bool

	// retryBase and retryAt hold the jittered push-retry schedule. They are
	// touched only by the uploader goroutine, so they need no lock.
	retryBase time.Duration
	retryAt   time.Time

	// Recovery state, uploader-goroutine only like the retry schedule.
	// consecutiveNoProgressRecoveries counts recoveries with no successful
	// push in between; lastRecoveryAt drives the interval refusal;
	// consecutiveCreateFailures and createThrottledUntil are the create
	// rate bound. See session_recover.go.
	consecutiveNoProgressRecoveries int
	lastRecoveryAt                  time.Time
	createThrottledUntil            time.Time
	// consecutiveCreateFailures and createRefusals are the create throttle's
	// counters: failed attempts, and recovery entries the throttle turned
	// away without a request. Written by the uploader only; atomic so a test
	// can read them while the uploader is parked on the retry schedule and
	// prove a refusal moved the second but not the first.
	consecutiveCreateFailures atomic.Int32
	createRefusals            atomic.Int32

	// createParams is the body recovery re-posts to mint a replacement
	// session: the same title and labels the run attached with.
	createParams CreateSessionParams
	telemetry    *SyncTelemetry
	uploadBatch  atomic.Uint64

	// beforeRecoveryLock is a test seam, nil in production. It runs after
	// recovery's CreateSession and before it takes s.mu, which is the one
	// window a concurrent terminal stop can land in.
	beforeRecoveryLock func()

	// lastGapBase is the server mark the last sequence-gap rebase used, or
	// noGapBase when none is outstanding. A second gap at the same mark proves
	// the rebase moved nothing, which is the loop guard on the rebase path.
	// Uploader-goroutine only, like the retry schedule.
	lastGapBase int64

	// mu guards the projector and the appender. Every projector mutation
	// happens under it: projection on the worker, and the two network-free
	// server-failure repairs on the uploader (rebaseOn's ResetSeq, recovery's
	// id swap and fork marker). No network call ever runs under it.
	mu      sync.Mutex
	stopCh  chan struct{}
	doneCh  chan struct{}
	eventCh chan events.Event
	// flushCh wakes the uploader. processEvent signals it after every
	// append; buffered 1, so a storm of signals coalesces into one wake and
	// the uploader batches whatever landed during its last round trip.
	flushCh chan struct{}
	// finalCh hands the uploader the shutdown deadline for its final flush,
	// after the worker has drained and appended the tail. uploaderDone
	// closes when the uploader has returned; the worker waits on it before
	// closing doneCh, so doneCh still means "nothing touches the outbox".
	finalCh      chan context.Context
	finalErr     atomic.Pointer[error]
	uploaderDone chan struct{}
	// shutdownDone closes after the timeout finalizer has finished closing the
	// outbox. Stop may return earlier when its caller deadline expires, but
	// owners that release the session's storage can wait for this signal.
	shutdownDone   chan struct{}
	uploaderCancel context.CancelFunc
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

	s := newSyncSession(localSessionID, activeSessionID, initialSeq, client, outbox, opts, params)

	if att.ForkedFrom != "" {
		// No worker exists yet, but the lock is taken anyway so one contract
		// serves both callers of rebaseOntoSessionLocked.
		s.mu.Lock()
		err := s.rebaseOntoSessionLocked(att.ForkedFrom)
		s.mu.Unlock()
		if err != nil {
			_ = outbox.Close()
			return nil, fmt.Errorf("apply forked attach: %w", err)
		}
	}
	// After the fork block: a refused open must not leave a healthy record.
	s.health.noteOpen(outbox.UnflushedCount())

	if opts.EnablePolling {
		s.poller = NewInputPoller(client, activeSessionID, opts.PollWaitSeconds, opts.AuthorUserIDProvider, opts.OutboxDir)
		s.poller.SetOnRejected(opts.OnInputRejected)
	}

	s.sub = bus.SubscribeAcross(syncKinds, s, events.SubscribeOptions{BufSize: 1024})
	s.dropSource = s.preProjectionDrops
	s.unsubscribe = s.sub.Unsubscribe

	s.heartbeat.Start(ctx)
	if s.poller != nil {
		s.poller.Start(ctx)
	}

	uploaderCtx, uploaderCancel := context.WithCancel(ctx)
	s.uploaderCancel = uploaderCancel
	go s.uploaderLoop(uploaderCtx)
	go s.workerLoop(ctx)

	return s, nil
}

// newSyncSession builds the session value with every channel, counter and
// seam in its starting state. Neither the worker nor the uploader is started
// here.
func newSyncSession(localID, remoteID string, initialSeq int64, client *Client, outbox *Outbox, opts SessionOptions, params CreateSessionParams) *SyncSession {
	s := &SyncSession{
		localSessionID: localID,
		sessionID:      remoteID,
		client:         client,
		outbox:         outbox,
		projector:      NewProjector(localID, initialSeq, opts.ProjectorOptions),
		heartbeat:      NewHeartbeatRunner(client, remoteID, opts.HeartbeatPeriod),
		opts:           opts,
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		eventCh:        make(chan events.Event, eventBufSize(opts.EventBufSize)),
		flushCh:        make(chan struct{}, 1),
		finalCh:        make(chan context.Context, 1),
		uploaderDone:   make(chan struct{}),
		shutdownDone:   make(chan struct{}),
		stopCtxCh:      make(chan context.Context, 1),
		lastGapBase:    noGapBase,
		health:         newSyncHealth(newStatusFileWriter(opts.OutboxDir)),
		createParams:   params,
		telemetry:      opts.Telemetry,
	}
	s.running.Store(true)
	s.appender = outbox
	return s
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

// Stopped reports whether sync has latched a terminal stop: a fatal auth
// failure, a poison 400, or a recovery bound. The local chat is unaffected
// either way.
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
