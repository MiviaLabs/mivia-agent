package coordinator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

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
	recovered          bool
	requestFingerprint string
	cancelOnce         sync.Once
	cancelDone         chan struct{}
	cancellationErr    error
	owner              *coordinator
	partial            bool
}

func (h *RunHandle) Done() <-chan struct{} { return h.done }

type RunResult struct {
	Snapshot ledger.RunSnapshot
	Results  []subagents.Result
	Err      error
}

type LifecycleSubscriber func(event ledger.LifecycleEvent)

type Coordinator interface {
	Spawn(context.Context, []subagents.Task, string, ...bool) (*RunHandle, error)
	Inspect(context.Context, *RunHandle) (ledger.RunSnapshot, error)
	Join(context.Context, *RunHandle) (*RunResult, error)
	Cancel(context.Context, *RunHandle) error
	SetTimeSource(func() time.Time)
	WithRetryPolicy(RetryPolicy) Coordinator
	ResumeInterruptedRun(context.Context, string) (*RunHandle, error)
	ListInterruptedRuns(context.Context) ([]RecoveredRun, error)
	SubscribeLifecycle(LifecycleSubscriber) func()
}

type coordinator struct {
	repo            ledger.LedgerRepository
	pool            *subagents.Pool
	names           *ledger.DisplayNameGenerator
	handles         map[string]*RunHandle
	handlesMu       sync.Mutex
	spawnMu         sync.Mutex
	now             func() time.Time
	nowMu           sync.RWMutex
	handleRetention time.Duration
	retryPolicy     RetryPolicy
	subscribers     []subscriberEntry
	subMu           sync.RWMutex
}

type subscriberEntry struct {
	id uint64
	fn LifecycleSubscriber
}

var subscriberIDCounter atomic.Uint64

func New(repo ledger.LedgerRepository, pool *subagents.Pool) Coordinator {
	return &coordinator{repo: repo, pool: pool, names: ledger.NewDisplayNameGenerator(), handles: map[string]*RunHandle{}, now: time.Now, handleRetention: 10 * time.Minute, retryPolicy: NoRetry}
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

func (c *coordinator) WithRetryPolicy(policy RetryPolicy) Coordinator {
	c.retryPolicy = policy
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
		func() { defer func() { recover() }(); fn(evt) }()
	}
}

var runIDCounter atomic.Uint64

func newRunID() string { return fmt.Sprintf("run-%d", runIDCounter.Add(1)) }

func AdvanceRunIDCounter(min uint64) {
	for {
		current := runIDCounter.Load()
		if current >= min {
			return
		}
		if runIDCounter.CompareAndSwap(current, min) {
			return
		}
	}
}

var eventIDCounter atomic.Uint64

func newEventID() string { return fmt.Sprintf("evt-%d", eventIDCounter.Add(1)) }

var taskIDCounter atomic.Uint64
var attemptIDCounter atomic.Uint64

func newAttemptID() string { return fmt.Sprintf("attempt-%d", attemptIDCounter.Add(1)) }
func newTaskID() string    { return fmt.Sprintf("task-%d", taskIDCounter.Add(1)) }
