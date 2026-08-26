package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// RunHandle is a handle to an active orchestration run.
type RunHandle struct {
	mu                 sync.RWMutex
	runID              string
	done               chan struct{}
	cancel             context.CancelFunc
	poolCtx            context.Context
	result             *RunResult
	attempts           map[string]string
	attemptsMu         sync.RWMutex // guards attempts: write from DAG goroutine, read from cancel goroutine
	recovered          bool
	localActor         bool
	requestFingerprint string
	retryPolicy        RetryPolicy
	failInterrupted    bool
	cancelOnce         sync.Once
	cancelDone         chan struct{}
	cancellationErr    error
	owner              *coordinator
	// nonInteractiveParent marks a run whose parent is a non-interactive
	// controller that can never answer child questions (set at construction;
	// immutable thereafter). ParkQuestion declines such runs' child questions
	// immediately at park time instead of parking and burning wait_seconds.
	nonInteractiveParent bool
	// mailboxes is parent→child delivery (plan 53.03). Context-only; never
	// fingerprinted. Guarded by its own mutex (mailboxes.mu), not h.mu.
	mailboxes *runMailboxes
	// referrals tracks in-flight referral-as-spawn tasks (plan 53.04).
	referrals *referralTracker
}

func (h *RunHandle) policy() RetryPolicy {
	if h == nil {
		return NoRetry
	}
	h.mu.RLock()
	p := h.retryPolicy
	h.mu.RUnlock()
	return p
}

func (h *RunHandle) mustFailInterrupted() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	v := h.failInterrupted
	h.mu.RUnlock()
	return v
}

func (h *RunHandle) Done() <-chan struct{} { return h.done }

// LocalActor reports whether this process owns execution of the run.
func (h *RunHandle) LocalActor() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	local := h.localActor
	h.mu.RUnlock()
	return local
}

// isNonInteractiveParent reports whether the run's parent cannot answer child
// questions. Locking accessor: the flag is written at construction before the
// run goroutine starts and never mutated, but ParkQuestion may be reached from
// any pool worker, so reads go through h.mu like poolContext().
func (h *RunHandle) isNonInteractiveParent() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nonInteractiveParent
}

// poolContext returns the run's pool context under lock so concurrent
// referral tasks do not race executeResumedRun's rewrite of poolCtx.
func (h *RunHandle) poolContext() context.Context {
	if h == nil {
		return context.Background()
	}
	h.mu.RLock()
	ctx := h.poolCtx
	h.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// setAttempt records the current attempt ID for a task. Must be called from
// the single writer goroutine (DAG execution). Concurrent with getAttempt
// from the cancel goroutine.
func (h *RunHandle) setAttempt(taskID, attemptID string) {
	h.attemptsMu.Lock()
	h.attempts[taskID] = attemptID
	h.attemptsMu.Unlock()
}

// getAttempt returns the current attempt ID for a task. Safe for concurrent
// use from any goroutine.
func (h *RunHandle) getAttempt(taskID string) string {
	h.attemptsMu.RLock()
	v := h.attempts[taskID]
	h.attemptsMu.RUnlock()
	return v
}

type RunResult struct {
	Snapshot ledger.RunSnapshot
	Results  []subagents.Result
	Err      error
}

type LifecycleSubscriber func(event ledger.LifecycleEvent)

type Coordinator interface {
	Spawn(context.Context, []subagents.Task, string) (*RunHandle, error)
	// SpawnNew is Spawn plus an isNew signal: false when the idempotency-key
	// lookup returned an existing run some other caller started, so a
	// caller can tell whether it is safe to treat itself as the run's sole
	// owner (e.g. before canceling it on its own unrelated context dying).
	SpawnNew(context.Context, []subagents.Task, string) (*RunHandle, bool, error)
	EnsureRun(context.Context, EnsureRunRequest) (*RunHandle, error)
	EnsureSingleTaskRun(context.Context, EnsureRunRequest) (*RunHandle, error)
	EnsureTerminalSingleTaskRun(context.Context, EnsureRunRequest, ledger.TaskStatus) (*RunHandle, error)
	// JoinAsRecovered returns a recovered, wait-only handle for an already
	// admitted run, or ledger.ErrNotFound if none is admitted yet. It never
	// claims to run the child, dispatches its handler, or resumes it as a
	// local actor: Cancel on the returned handle always takes the fail-closed
	// recovered path, which refuses when a task's persisted status looks
	// nonterminal with no verifiable live owner.
	JoinAsRecovered(context.Context, EnsureRunRequest) (*RunHandle, error)
	Inspect(context.Context, *RunHandle) (ledger.RunSnapshot, error)
	Join(context.Context, *RunHandle) (*RunResult, error)
	Cancel(context.Context, *RunHandle) error
	SetTimeSource(func() time.Time)
	WithRetryPolicy(RetryPolicy) Coordinator
	ResumeInterruptedRun(context.Context, string) (*RunHandle, error)
	ListInterruptedRuns(context.Context) ([]RecoveredRun, error)
	SubscribeLifecycle(LifecycleSubscriber) func()
	// PostTaskMessage persists a typed agent message and announces a
	// task_message lifecycle event (ID + synopsis only). Plan 53.01 seam.
	PostTaskMessage(ctx context.Context, runID, taskID string, msg agentmsg.Message) error
	// ParkQuestion / DeliverAnswer / Transition* support plan 53.02 questions.
	// maxWait is the asker's effective max wait; the park expires at
	// max(parkTTL, maxWait+parkSlack) so long waits are never evicted early.
	ParkQuestion(runID, taskID, messageID string, maxWait ...time.Duration) (answerCh <-chan string, unpark func(), err error)
	// DeliverAnswer unblocks a park when inReplyTo matches the parked message id
	// (empty inReplyTo matches any live park for the task).
	DeliverAnswer(runID, taskID, inReplyTo, body string) bool
	TransitionToAwaitingInput(ctx context.Context, runID, taskID string) error
	TransitionFromAwaitingInput(ctx context.Context, runID, taskID, newStatus string) error
	ConsumeMessageQuota(runID, taskID string, max int) error
	// RefundMessageQuota decrements the per-task upstream message count after a
	// failed persist so a failed message never permanently burns a budget slot
	// (messageQuota is otherwise increment-only). Floored at zero: it only ever
	// undoes a prior ConsumeMessageQuota.
	RefundMessageQuota(runID, taskID string)
	CountPendingQuestions(runID, taskID string) int
	// ParkedQuestions returns the live parked questions for a run
	// (TaskID/MessageID/ExpiresAt), read under the question registry lock.
	// Expired parks are treated as absent via the existing eviction.
	ParkedQuestions(runID string) []ParkedQuestion
	ListRunMessages(ctx context.Context, runID, taskID string) ([]MessageSummary, error)
	LoadMessageBody(ctx context.Context, contentRef string) (agentmsg.Message, error)
	// SendToTask enqueues a parent→child message (steer/answer) after ledger persist.
	SendToTask(ctx context.Context, h *RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error)
	// WithMessagingLimits applies body/mailbox budgets from [subagents.messaging].
	WithMessagingLimits(maxBodyBytes, mailboxCapacity int) Coordinator
	// Ask registry (plan 53.04).
	RegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string)
	TryRegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string, maxAsks int) bool
	AsksUsedByTask(runID, taskID string) int
	ReferralSpawnsUsed(runID string) int
	IncReferralSpawn(runID string)
	TryIncReferralSpawn(runID string, max int) bool
	DecReferralSpawn(runID string)
	AskLookup(askID string) (askerTaskID string, ok bool)
	AskChainInfo(parentAskID, toRole string) (depth int, cycle bool, ancestors []string)
	CompleteAskAnswer(askID string) error
	ClaimAskAnswer(askID string) (askerTaskID string, err error)
	// BeginAskAnswer claims an open registry ask for parent/peer one-shot.
	// claimed=false,err=nil means not a registry ask (question path).
	BeginAskAnswer(askID string) (askerTaskID string, claimed bool, err error)
	IsAskAnswered(askID string) bool
	CloseAsk(askID string)
	// SealAskAnswer closes open/claimed ask; true only if this call sealed.
	SealAskAnswer(askID string) bool
	UnclaimAskAnswer(askID, askerTaskID string)
	// FindLiveTaskByRole returns a running/awaiting task whose AgentName matches role.
	FindLiveTaskByRole(ctx context.Context, runID, role string) (taskID string, ok bool, err error)
	// HandleForRun returns the in-memory handle for an active run, if any.
	HandleForRun(runID string) *RunHandle
	// MailboxSend delivers an already-persisted message to a task mailbox.
	MailboxSend(h *RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error)
	// SpawnReferralFromAsk starts a same-run referral task for a non-blocking ask.
	// Optional meta supplies agent digest/provider/model for production agents.
	SpawnReferralFromAsk(ctx context.Context, runID, toRole string, ask agentmsg.Message, meta ...ReferralSpawnMeta) (taskID string, err error)
	// SpawnReferral starts a same-run task by role/name with the given input.
	// askID, when non-empty, is bound before the referral goroutine starts.
	SpawnReferral(ctx context.Context, runID string, task subagents.Task, askID string) (taskID string, err error)
}

type coordinator struct {
	repo            ledger.LedgerRepository
	pool            *subagents.Pool
	names           *ledger.DisplayNameGenerator
	handles         map[string]*RunHandle // idempotency key → handle
	handlesByRun    map[string]*RunHandle // runID → handle (for messaging delivery)
	handlesMu       sync.Mutex
	spawnMu         sync.Mutex
	resumeMu        sync.Mutex // serializes resume admission within this coordinator
	holderID        string     // random per-process ID for run execution claims
	claimLease      time.Duration
	claimHeartbeat  time.Duration
	now             func() time.Time
	nowMu           sync.RWMutex
	retryMu         sync.RWMutex
	handleRetention time.Duration
	retryPolicy     RetryPolicy
	subscribers     []subscriberEntry
	subMu           sync.RWMutex
	// questions tracks parked child questions (plan 53.02).
	questions *questionRegistry
	// msgQuota tracks per-task upstream message counts.
	msgQuota *messageQuota
	// asks tracks open peer asks and one-answer enforcement (plan 53.04).
	asks *askRegistry
	// maxBodyBytes bounds message bodies at PostTaskMessage (plan 53 messaging).
	// Zero means agentmsg.DefaultMaxBodyBytes.
	maxBodyBytes int
	// mailboxCapacity is parent→child mailbox depth (plan 53.03). Zero → 32.
	mailboxCapacity int
}

type subscriberEntry struct {
	id uint64
	fn LifecycleSubscriber
}

var subscriberIDCounter atomic.Uint64

func New(repo ledger.LedgerRepository, pool *subagents.Pool) Coordinator {
	c := &coordinator{
		repo: repo, pool: pool, names: ledger.NewDisplayNameGenerator(),
		handles: map[string]*RunHandle{}, handlesByRun: map[string]*RunHandle{},
		holderID:   newCoordinatorHolderID(),
		claimLease: defaultRunClaimLease, claimHeartbeat: defaultRunClaimLease / 3,
		now: time.Now, handleRetention: 10 * time.Minute, retryPolicy: DefaultRetryPolicy,
		// Pre-allocate so ParkQuestion / CountPendingQuestions never race on
		// lazy nil-init of the questions pointer (plan 53.02 concurrency).
		questions:       &questionRegistry{byKey: map[string]*pendingQuestion{}},
		msgQuota:        &messageQuota{count: map[string]int{}},
		asks:            newAskRegistry(),
		maxBodyBytes:    agentmsg.DefaultMaxBodyBytes,
		mailboxCapacity: 32,
	}
	if pool != nil && pool.ContextForTask == nil {
		// Install once; pure function of parent context (safe under concurrent runs).
		pool.ContextForTask = contextForTask
	}
	if pool != nil {
		// Install the per-task completion hook so a terminal task is finalized
		// early (ledger status CAS + mailbox fence + ask decline) from the pool
		// worker the moment its handler returns, instead of waiting for the
		// whole pool to finish (plan R9). c.onTaskDone is nil-safe and
		// idempotent; recordRunResults still owns output/attempt persistence
		// and the single terminal event.
		pool.OnTaskDone = c.onTaskDone
	}
	return c
}

// WithMessagingLimits applies [subagents.messaging] body and mailbox budgets.
// Non-positive values leave the current setting unchanged. Safe to call on the
// concrete coordinator returned by New before the first Spawn.
func (c *coordinator) WithMessagingLimits(maxBodyBytes, mailboxCapacity int) Coordinator {
	if maxBodyBytes > 0 {
		c.maxBodyBytes = maxBodyBytes
	}
	if mailboxCapacity > 0 {
		c.mailboxCapacity = mailboxCapacity
	}
	return c
}

// newCoordinatorHolderID generates a random per-process identifier for run
// execution claims. crypto/rand.Read never returns an error and always fills
// its buffer - it crashes the program itself if the operating system's source
// fails - so there is no error to handle here. That is also the only acceptable
// outcome: a claim holder derived from a degraded source would be guessable,
// and no fallback source is safe to substitute.
func newCoordinatorHolderID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "c-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

func (c *coordinator) SetTimeSource(now func() time.Time) {
	c.nowMu.Lock()
	c.now = now
	c.nowMu.Unlock()
}

func (c *coordinator) nowLocked() time.Time {
	c.nowMu.RLock()
	now := c.now()
	c.nowMu.RUnlock()
	return now
}

func (c *coordinator) retryPolicyLocked() RetryPolicy {
	c.retryMu.RLock()
	p := c.retryPolicy
	c.retryMu.RUnlock()
	return p
}

func (c *coordinator) WithRetryPolicy(policy RetryPolicy) Coordinator {
	c.retryMu.Lock()
	c.retryPolicy = policy
	c.retryMu.Unlock()
	return c
}

var _ Coordinator = (*coordinator)(nil)

func (c *coordinator) SubscribeLifecycle(fn LifecycleSubscriber) func() {
	if fn == nil {
		return func() {}
	}
	id := subscriberIDCounter.Add(1)
	c.subMu.Lock()
	c.subscribers = append(c.subscribers, subscriberEntry{id: id, fn: fn})
	c.subMu.Unlock()
	return func() {
		c.subMu.Lock()
		defer c.subMu.Unlock()
		for i := range c.subscribers {
			if c.subscribers[i].id == id {
				c.subscribers = append(c.subscribers[:i], c.subscribers[i+1:]...)
				return
			}
		}
	}
}

func (c *coordinator) emitLifecycleEvent(evt ledger.LifecycleEvent) {
	c.subMu.RLock()
	safe := make([]LifecycleSubscriber, len(c.subscribers))
	for i, entry := range c.subscribers {
		safe[i] = entry.fn
	}
	c.subMu.RUnlock()
	for _, fn := range safe {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("panic in lifecycle subscriber for event %s (run=%s kind=%s task=%s attempt=%s): %v",
						evt.ID, evt.RunID, evt.Kind, evt.TaskID, evt.AttemptID, r)
				}
			}()
			fn(evt)
		}()
	}
}

// NewRunID returns an unguessable run identifier. Unguessability is load-bearing
// (INV-AG-9): run IDs must not be enumerable. crypto/rand.Read never returns an
// error and always fills its buffer, crashing the program if the operating
// system's source fails, so there is no error path - and no weaker fallback
// would be acceptable if there were.
func NewRunID() string {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return "run-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(token[:])
}

func newRunID() string { return NewRunID() }

var eventIDCounter atomic.Uint64

func newEventID() string { return fmt.Sprintf("evt-%d", eventIDCounter.Add(1)) }

var taskIDCounter atomic.Uint64
var attemptIDCounter atomic.Uint64

func newAttemptID() string { return fmt.Sprintf("attempt-%d", attemptIDCounter.Add(1)) }
func newTaskID() string    { return fmt.Sprintf("task-%d", taskIDCounter.Add(1)) }
