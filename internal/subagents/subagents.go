// Package subagents provides bounded dependency-aware execution.
package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"log"
	"sort"
	"sync"
	"time"
)

type Task struct {
	ID, Name, Owner string
	// AgentName and AgentDigest identify the immutable authorized definition.
	// Name is a private runtime target and never comes from model input.
	AgentName, AgentDigest, Skill string
	// ProviderName and Model describe the resolved work binding. Current policy
	// re-authorizes them before a resumed task executes.
	// ProviderName and Model ARE included in the coordinator fingerprint
	// projection (spawn.go), so adding or changing them here WILL change
	// idempotency digests for agent-routed tasks. Delegate/oneshot tasks
	// carry empty values so are unaffected by these fields.
	ProviderName, Model string
	// SessionID, TurnID, and Role retain caller identity across asynchronous
	// coordinator execution so nested tool calls remain attributable.
	SessionID, TurnID, Role string
	// InvocationKey scopes dispatcher idempotency independently from the
	// user-facing task ID, which may repeat across batches.
	InvocationKey         string
	DependsOn             []string
	Scope                 string
	Permission            string
	Input                 json.RawMessage
	Depth                 int
	Timeout               time.Duration
	Budget                int
	WorkLimits            runtime.WorkLimits
	DisableProviderReplay bool
	IdempotencyKey        string
	// OutputSchema, when non-nil, is the resolved JSON Schema the child's final
	// reply must satisfy (plan tools/02). Nil means free-text output (today's
	// contract). Work-defining: included in the coordinator fingerprint.
	OutputSchema map[string]any
	// InputSchema, when non-nil, validates Task.Input at admission.
	InputSchema map[string]any
}
type Result struct {
	TaskID     string
	Output     json.RawMessage
	Err        error
	Status     string
	Provenance runtime.Metadata
}
type Policy struct {
	Workers, MaxDepth, MaxFanout int
	MaxBudget                    int
	Timeout                      time.Duration
}

// Unlimited is the sentinel value that explicitly requests no limit.
// Policy fields default to safe non-zero values in New(); use Unlimited
// to opt out of the default bound.
const Unlimited = -1

// Default safe limits applied when Policy fields are zero (unconfigured).
// Zero must not mean unlimited: a missing config should degrade to safe bounds,
// not to unbounded fan-out or budget.
const (
	DefaultMaxFanout = 32
	DefaultMaxDepth  = 10
	DefaultMaxBudget = 1000
)

type Pool struct {
	d *runtime.Dispatcher
	p Policy
	// ContextForTask, when set, derives a per-task context from the pool
	// context before dispatch (plan 53). Used to inject task identity and
	// (phase 03) mailbox handles without fingerprinted Task fields.
	ContextForTask func(ctx context.Context, taskID string) context.Context
	// OnTaskDone, when set, is invoked on the worker goroutine immediately
	// after a task's handler returns, with the STAMPED per-task context
	// (ContextForTask has already applied TaskIdentity{RunID, TaskID, Agent})
	// and the computed result (status, output, error). The coordinator uses it
	// to finalize terminal tasks early — CAS the ledger status, mark the
	// mailbox terminal, and decline parked asks — instead of waiting for the
	// whole pool to finish (plan R9). The result value returned to the caller
	// is never modified by the callback; nil means no-op.
	OnTaskDone func(ctx context.Context, t Task, r Result)
}

// MaxFanout returns the maximum number of tasks accepted in one orchestration.
func (p *Pool) MaxFanout() int { return p.p.MaxFanout }

// MaxDepth returns the maximum dependency depth accepted by the pool.
func (p *Pool) MaxDepth() int { return p.p.MaxDepth }

// MaxBudget and Timeout expose the pool ceilings so a caller restoring
// persisted limits can clamp them rather than trust them (plan 12 §3).
func (p *Pool) MaxBudget() int         { return p.p.MaxBudget }
func (p *Pool) Timeout() time.Duration { return p.p.Timeout }

// ValidateTask checks an execution request without scheduling it. Resume uses
// this before durable state changes so a stale agent snapshot fails closed.
func (p *Pool) ValidateTask(t Task) error {
	if p == nil || p.d == nil {
		return fmt.Errorf("nil subagent pool")
	}
	return p.d.Validate(runtime.Request{
		ID: t.ID, ParentID: t.Owner, Name: t.Name, Kind: runtime.Subagent,
		SessionID: t.SessionID, TurnID: t.TurnID, Role: t.Role, Scope: t.Scope,
		Permission: t.Permission, Input: t.Input, Budget: t.Budget, Depth: t.Depth,
		Timeout: t.Timeout, AgentName: t.AgentName, AgentDigest: t.AgentDigest, Skill: t.Skill,
		ProviderName: t.ProviderName, Model: t.Model,
		WorkLimits: t.WorkLimits, DisableProviderReplay: t.DisableProviderReplay,
	})
}

func New(d *runtime.Dispatcher, p Policy) *Pool {
	// Apply safe defaults for zero-valued limits. Zero must not mean unlimited;
	// an unconfigured deployment should degrade to safe bounds rather than
	// unbounded fan-out or budget. Use Unlimited (-1) to explicitly opt out.
	if p.MaxFanout == 0 {
		p.MaxFanout = DefaultMaxFanout
	}
	if p.MaxDepth == 0 {
		p.MaxDepth = DefaultMaxDepth
	}
	if p.MaxBudget == 0 {
		p.MaxBudget = DefaultMaxBudget
	}
	return &Pool{d: d, p: p}
}

func (p *Pool) validate(tasks []Task) (map[string]Task, error) {
	if p.p.MaxFanout != Unlimited && p.p.MaxFanout > 0 && len(tasks) > p.p.MaxFanout {
		return nil, fmt.Errorf("fan-out limit exceeded")
	}
	by := map[string]Task{}
	keys := map[string]string{}
	invocationKeys := map[string]string{}
	total := 0
	for _, t := range tasks {
		if t.ID == "" || by[t.ID].ID != "" {
			return nil, fmt.Errorf("duplicate task id")
		}
		if t.Budget < 0 {
			return nil, fmt.Errorf("budget must be non-negative")
		}
		by[t.ID] = t
		total += t.Budget
		if t.IdempotencyKey != "" {
			if old, ok := keys[t.IdempotencyKey]; ok && old != t.ID {
				return nil, fmt.Errorf("idempotency key collision")
			}
			keys[t.IdempotencyKey] = t.ID
		}
		if t.InvocationKey != "" {
			if old, ok := invocationKeys[t.InvocationKey]; ok && old != t.ID {
				return nil, fmt.Errorf("invocation key collision")
			}
			invocationKeys[t.InvocationKey] = t.ID
		}
		if p.p.MaxDepth != Unlimited && p.p.MaxDepth > 0 && t.Depth > p.p.MaxDepth {
			return nil, fmt.Errorf("depth limit exceeded")
		}
		if p.p.MaxBudget != Unlimited && p.p.MaxBudget > 0 && t.Budget > p.p.MaxBudget {
			return nil, fmt.Errorf("budget limit exceeded")
		}
	}
	if p.p.MaxBudget != Unlimited && p.p.MaxBudget > 0 && total > p.p.MaxBudget {
		return nil, fmt.Errorf("run budget exceeded")
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := by[dep]; !ok {
				return nil, fmt.Errorf("missing dependency %q", dep)
			}
		}
	}
	return by, nil
}

// ready returns tasks whose dependencies are all resolved. Tasks with a
// failed dependency are marked blocked and removed from pending.
//
// The function iterates pending to a fixpoint. Each pass blocks tasks whose
// dependencies now carry failed results, then the next pass blocks their
// dependents. Without the fixpoint, a single map pass could visit a dependent
// before its newly blocked dependency. The dependent stays pending, and run()
// reports a false "dependency cycle" while the dependent is reported "missing"
// instead of "blocked". The fixpoint makes the outcome independent of map
// order (DC-9).
//
// When called from the coordinator, all task dependencies are nil'd (see
// coordinator/dag.go buildBatch), so this function always returns all pending
// tasks as ready. The coordinator owns dependency resolution via its own
// collectReady() which also handles ledger state transitions.
//
// This function is retained for standalone Pool.Run() callers (tests and
// future non-coordinator use) that pass tasks with live DependsOn.
func ready(pending map[string]Task, results map[string]Result) ([]Task, error) {
	out := []Task{}
	// emitted tracks ready tasks already reported. Ready tasks stay in pending:
	// run() breaks when pending is empty, so deleting them here would skip their
	// execution. The set stops a later pass from reporting one ready task twice.
	emitted := make(map[string]bool, len(pending))
	for changed := true; changed; {
		changed = false
		for id, t := range pending {
			if emitted[id] {
				continue
			}
			blocked := ""
			ok := true
			for _, dep := range t.DependsOn {
				r, done := results[dep]
				if !done {
					ok = false
				} else if r.Err != nil {
					blocked = dep
				}
			}
			if blocked != "" {
				// Record and continue. Aborting the scheduler here would deny the caller
				// results for every task that had already finished, which is the whole
				// reason the old partial_results knob existed - and it never protected
				// anything, because this branch was unreachable from the coordinator.
				delete(pending, id)
				results[id] = Result{TaskID: id, Status: "blocked", Err: fmt.Errorf("dependency %s failed", blocked)}
				changed = true
				continue
			}
			if ok {
				out = append(out, t)
				emitted[id] = true
				changed = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (p *Pool) execute(ctx context.Context, tasks []Task, results map[string]Result) {
	jobs := make(chan Task, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	workers := p.p.Workers
	if workers == 0 {
		// 0 = unlimited: one worker per task (bounded by len(tasks)).
		workers = len(tasks)
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				r := p.safeExecuteOne(ctx, t)
				mu.Lock()
				results[t.ID] = r
				mu.Unlock()
			}
		}()
	}
	for _, t := range tasks {
		select {
		case jobs <- t:
		case <-ctx.Done():
			mu.Lock()
			results[t.ID] = Result{TaskID: t.ID, Err: ctx.Err(), Status: "canceled"}
			mu.Unlock()
		}
	}
	close(jobs)
	wg.Wait()
}

// safeExecuteOne wraps executeOne with per-task panic recovery. Recovery is
// scoped to this one call (not the whole worker goroutine): a goroutine-level
// recover would unwind past the `for t := range jobs` loop and end that
// worker early, silently shrinking pool parallelism for the rest of the
// batch. A panic from one task must cost that task alone.
func (p *Pool) safeExecuteOne(ctx context.Context, t Task) (r Result) {
	defer func() {
		if rec := recover(); rec != nil {
			r = Result{TaskID: t.ID, Err: fmt.Errorf("subagent task %q panicked: %v", t.ID, rec), Status: "failed"}
		}
	}()
	return p.executeOne(ctx, t)
}

func (p *Pool) executeOne(ctx context.Context, t Task) Result {
	if ctx.Err() != nil {
		return Result{TaskID: t.ID, Err: ctx.Err(), Status: "canceled"}
	}
	// Enforce task/policy timeout at the pool layer (defense in depth).
	// Handlers and the dispatcher also see Request.Timeout.
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = p.p.Timeout
	}
	baseCtx := ctx
	if p.ContextForTask != nil {
		baseCtx = p.ContextForTask(ctx, t.ID)
	}
	taskCtx := baseCtx
	cancel := func() {}
	if timeout > 0 {
		taskCtx, cancel = context.WithTimeout(baseCtx, timeout)
	} else {
		taskCtx, cancel = context.WithCancel(baseCtx)
	}
	defer cancel()
	// Task.ID is caller-facing coordination state. It must not cross the
	// dispatch boundary: concurrent runs are allowed to reuse display IDs,
	// while the dispatcher requires a fresh opaque invocation identity.
	id := runtime.NewSessionID()
	r := p.d.Invoke(taskCtx, runtime.Request{
		ID: id, ParentID: t.Owner, Name: t.Name, Kind: runtime.Subagent,
		SessionID: t.SessionID, TurnID: t.TurnID, Role: t.Role,
		Scope: t.Scope, Permission: t.Permission, Input: t.Input,
		AgentName: t.AgentName, AgentDigest: t.AgentDigest, Skill: t.Skill,
		ProviderName: t.ProviderName, Model: t.Model,
		Budget: t.Budget, Depth: t.Depth, Timeout: timeout,
		WorkLimits: t.WorkLimits, DisableProviderReplay: t.DisableProviderReplay,
		OutputSchema: t.OutputSchema,
	})
	s := "completed"
	if r.Err != nil {
		s = resultStatus(taskCtx, ctx, r.Err)
	}
	result := Result{TaskID: t.ID, Output: r.Output, Err: r.Err, Status: s, Provenance: r.Metadata}
	if p.OnTaskDone != nil {
		// Finalize hook (plan R9): runs on the worker goroutine with the
		// stamped per-task context so the coordinator can fence a terminal
		// task before the rest of the pool finishes. It must not change the
		// result returned to the caller - callOnTaskDoneSafely enforces that
		// even when the hook panics (bug audit: safeExecuteOne's outer recover
		// covers this whole function, so a naive direct call here would let an
		// OnTaskDone panic overwrite an already-computed, correct result with
		// a synthetic "failed" one).
		callOnTaskDoneSafely(p.OnTaskDone, taskCtx, t, result)
	}
	return result
}

// callOnTaskDoneSafely invokes fn and recovers a panic without letting it
// propagate or alter the result the caller already computed. OnTaskDone's
// documented contract is that it never changes the returned result; a panic
// must not silently break that contract by falling through to
// safeExecuteOne's outer recover, which would replace a real (possibly
// successful) result with a synthetic failure.
func callOnTaskDoneSafely(fn func(context.Context, Task, Result), ctx context.Context, t Task, r Result) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("subagents: OnTaskDone panicked for task %q: %v", t.ID, rec)
		}
	}()
	fn(ctx, t, r)
}

func resultStatus(taskCtx, parentCtx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(taskCtx.Err(), context.Canceled) || parentCtx.Err() != nil {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "failed"
}

// Run executes tasks and always returns one result per task, each carrying its own
// status, alongside any run-level error. There is no mode that returns less: a
// caller that asked for work wants to know what happened to all of it.
func (p *Pool) Run(ctx context.Context, tasks []Task) ([]Result, error) {
	return p.run(ctx, tasks)
}

func (p *Pool) run(ctx context.Context, tasks []Task) ([]Result, error) {
	by, err := p.validate(tasks)
	if err != nil {
		return nil, err
	}
	pending := map[string]Task{}
	for id, t := range by {
		pending[id] = t
	}
	results := map[string]Result{}
	var runErr error
	for len(pending) > 0 {
		batch, err := ready(pending, results)
		if err != nil {
			return collectResults(tasks, results), err
		}
		if len(pending) == 0 {
			break
		}
		if len(batch) == 0 {
			return collectResults(tasks, results), fmt.Errorf("dependency cycle")
		}
		for _, t := range batch {
			delete(pending, t.ID)
		}
		p.execute(ctx, batch, results)
		if ctx.Err() != nil {
			// Mark any tasks still pending as canceled, keep completed results.
			for id, t := range pending {
				if _, ok := results[id]; !ok {
					results[id] = Result{TaskID: t.ID, Err: ctx.Err(), Status: "canceled"}
				}
				delete(pending, id)
			}
			runErr = ctx.Err()
			break
		}
	}
	return collectResults(tasks, results), runErr
}

func collectResults(tasks []Task, results map[string]Result) []Result {
	out := make([]Result, 0, len(tasks))
	for _, t := range tasks {
		if r, ok := results[t.ID]; ok {
			out = append(out, r)
		} else {
			out = append(out, Result{TaskID: t.ID, Status: "missing"})
		}
	}
	return out
}
