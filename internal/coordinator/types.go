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

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// RunHandle is a handle to an active orchestration run.
type RunHandle struct {
	mu         sync.RWMutex
	runID      string
	done       chan struct{}
	cancel     context.CancelFunc
	poolCtx    context.Context
	result     *RunResult
	attempts   map[string]string
	attemptsMu sync.RWMutex // guards attempts: write from DAG goroutine, read from cancel goroutine
	// taskCancels holds each dispatched task's own CancelFunc, keyed by task
	// ID: onTaskStart (task_start.go) populates it from the per-task
	// cancelable context executeOne already derives for timeout enforcement
	// (subagents.Pool.executeOne). A task with no entry has not yet been
	// dispatched to the pool (still queued) — CancelTask (cancel_task.go)
	// cancels such a task through the ledger alone.
	taskCancels map[string]context.CancelFunc
	// taskDoneCh mirrors taskCancels: a fresh channel per dispatch attempt,
	// closed by onTaskDone (task_done.go) when that attempt's executeOne
	// call returns, so CancelTask can wait for that ONE task's own execution
	// to unwind after canceling its context, instead of waiting on h.done
	// (the WHOLE run's completion signal).
	taskDoneCh map[string]chan struct{}
	// taskCancelMu guards taskCancels and taskDoneCh: written from pool
	// worker goroutines (one per dispatched task, so concurrent across
	// siblings), read from CancelTask's caller goroutine.
	taskCancelMu sync.RWMutex
	// subagentToolCancelers holds, per task, the agent.ToolCanceler its own
	// nested SDK-backed loop published via agent.Options.OnToolCancelReady
	// (subagents.MultiStepHandler.OnToolCancelReady - see tool_cancel.go).
	// This is a SEPARATE registry from taskCancels above and deliberately
	// not conflated with it: taskCancels stops a task's WHOLE execution
	// context (used by CancelTask), while this registry reaches INSIDE a
	// still-running task to cancel just one of its tool calls, leaving
	// that task - and every sibling task, and the run itself - running.
	// Guarded by its own mutex rather than taskCancelMu so the two
	// registries never contend with each other.
	subagentToolCancelers map[string]agent.ToolCanceler
	subagentToolCancelMu  sync.RWMutex
	recovered             bool
	localActor            bool
	requestFingerprint    string
	retryPolicy           RetryPolicy
	failInterrupted       bool
	cancelOnce            sync.Once
	cancelDone            chan struct{}
	cancellationErr       error
	owner                 *Coordinator
	// nonInteractiveParent marks a run whose parent is a non-interactive
	// controller that can never answer child questions (set at construction;
	// immutable thereafter). ParkQuestion declines such runs' child questions
	// immediately at park time instead of parking and burning wait_seconds.
	nonInteractiveParent bool
	// mailboxes is parent→child delivery (plan 53.03). Context-only; never
	// fingerprinted. Guarded by its own mutex (mailboxes.mu), not h.mu.
	mailboxes *runMailboxes
	// toolCalls buffers per-task raw tool-call lifecycle steps for ledger
	// persistence at task finalize (Part B, chunk 4). Context-only; never
	// fingerprinted.
	toolCalls *runToolCallBuffer
	// referrals tracks in-flight referral-as-spawn tasks (plan 53.04).
	referrals *referralTracker
}

type RunResult struct {
	Snapshot ledger.RunSnapshot
	Results  []subagents.Result
	Err      error
}

type LifecycleSubscriber func(event ledger.LifecycleEvent)

type Coordinator struct {
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

func New(repo ledger.LedgerRepository, pool *subagents.Pool) *Coordinator {
	c := &Coordinator{
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
		// Install the per-task start hook so a task's own cancelable
		// execution context (the pool's per-task WithTimeout/WithCancel
		// derivation in executeOne, already used for timeout enforcement) is
		// registered on the run handle the moment it is dispatched. This is
		// what makes single-task cancellation (CancelTask, cancel_task.go)
		// structurally possible: without it, there is no per-task CancelFunc
		// to invoke, only the run-wide one.
		pool.OnTaskStart = c.onTaskStart
		// Install the pre-dispatch cancellation fence so a task canceled
		// after the DAG put it in a batch, but before a worker reached it,
		// never runs its handler at all (task_start.go
		// shouldSkipCanceledTask). Without it CancelTask reports success for
		// such a task while the task goes on doing real work.
		pool.ShouldSkipTask = c.shouldSkipCanceledTask
	}
	return c
}

// WithMessagingLimits applies [subagents.messaging] body and mailbox budgets.
// Non-positive values leave the current setting unchanged. Safe to call on the
// concrete coordinator returned by New before the first Spawn.
func (c *Coordinator) WithMessagingLimits(maxBodyBytes, mailboxCapacity int) *Coordinator {
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

func (c *Coordinator) SetTimeSource(now func() time.Time) {
	c.nowMu.Lock()
	c.now = now
	c.nowMu.Unlock()
}

func (c *Coordinator) nowLocked() time.Time {
	c.nowMu.RLock()
	now := c.now()
	c.nowMu.RUnlock()
	return now
}

func (c *Coordinator) retryPolicyLocked() RetryPolicy {
	c.retryMu.RLock()
	p := c.retryPolicy
	c.retryMu.RUnlock()
	return p
}

func (c *Coordinator) WithRetryPolicy(policy RetryPolicy) *Coordinator {
	c.retryMu.Lock()
	c.retryPolicy = policy
	c.retryMu.Unlock()
	return c
}

func (c *Coordinator) SubscribeLifecycle(fn LifecycleSubscriber) func() {
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

func (c *Coordinator) emitLifecycleEvent(evt ledger.LifecycleEvent) {
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
