// Package subagents provides bounded dependency-aware execution.
package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"sort"
	"sync"
	"time"
)

type Task struct {
	ID, Name, Owner string
	// InvocationKey scopes dispatcher idempotency independently from the
	// user-facing task ID, which may repeat across batches.
	InvocationKey  string
	DependsOn      []string
	Scope          string
	Permission     string
	Input          json.RawMessage
	Depth          int
	Timeout        time.Duration
	Budget         int
	IdempotencyKey string
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
	Partial                      bool
}
type Pool struct {
	d *runtime.Dispatcher
	p Policy
}

// MaxFanout returns the maximum number of tasks accepted in one orchestration.
func (p *Pool) MaxFanout() int { return p.p.MaxFanout }

// MaxDepth returns the maximum dependency depth accepted by the pool.
func (p *Pool) MaxDepth() int { return p.p.MaxDepth }

func New(d *runtime.Dispatcher, p Policy) *Pool {
	if p.Workers <= 0 {
		p.Workers = 4
	}
	if p.MaxDepth <= 0 {
		p.MaxDepth = 8
	}
	if p.MaxFanout <= 0 {
		p.MaxFanout = 64
	}
	return &Pool{d: d, p: p}
}

func (p *Pool) validate(tasks []Task) (map[string]Task, error) {
	if len(tasks) > p.p.MaxFanout {
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
		if t.Depth > p.p.MaxDepth {
			return nil, fmt.Errorf("depth limit exceeded")
		}
		if p.p.MaxBudget > 0 && t.Budget > p.p.MaxBudget {
			return nil, fmt.Errorf("budget limit exceeded")
		}
		for _, dep := range t.DependsOn {
			if _, ok := by[dep]; !ok { /* checked after all IDs are known */
			}
		}
	}
	if p.p.MaxBudget > 0 && total > p.p.MaxBudget {
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
func ready(pending map[string]Task, results map[string]Result, partial bool) ([]Task, error) {
	out := []Task{}
	for id, t := range pending {
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
			if !partial {
				return nil, fmt.Errorf("dependency %s failed for %s", blocked, id)
			}
			delete(pending, id)
			results[id] = Result{TaskID: id, Status: "blocked", Err: fmt.Errorf("dependency %s failed", blocked)}
			continue
		}
		if ok {
			out = append(out, t)
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
				r := p.executeOne(ctx, t)
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
	taskCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		taskCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		taskCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	id := t.IdempotencyKey
	if id == "" {
		id = t.InvocationKey
	}
	if id == "" {
		id = t.ID
	}
	r := p.d.Invoke(taskCtx, runtime.Request{
		ID: id, ParentID: t.Owner, Name: t.Name, Kind: runtime.Subagent,
		Scope: t.Scope, Permission: t.Permission, Input: t.Input,
		Budget: t.Budget, Depth: t.Depth, Timeout: timeout,
	})
	s := "completed"
	if r.Err != nil {
		s = resultStatus(taskCtx, ctx, r.Err)
	}
	return Result{TaskID: t.ID, Output: r.Output, Err: r.Err, Status: s, Provenance: r.Metadata}
}

func resultStatus(taskCtx, parentCtx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	if taskCtx.Err() == context.DeadlineExceeded {
		return "timed_out"
	}
	if taskCtx.Err() == context.Canceled || parentCtx.Err() != nil {
		return "canceled"
	}
	if err == context.DeadlineExceeded {
		return "timed_out"
	}
	if err == context.Canceled {
		return "canceled"
	}
	return "failed"
}

func (p *Pool) Run(ctx context.Context, tasks []Task) ([]Result, error) {
	return p.run(ctx, tasks, p.p.Partial)
}

// RunWithPartial runs tasks with an explicit partial override.
// When partial is true, the pool continues processing remaining tasks even
// when some tasks fail or the context is canceled, returning partial results.
func (p *Pool) RunWithPartial(ctx context.Context, tasks []Task, partial bool) ([]Result, error) {
	return p.run(ctx, tasks, partial)
}

func (p *Pool) run(ctx context.Context, tasks []Task, partial bool) ([]Result, error) {
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
		batch, err := ready(pending, results, partial)
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
			if !partial {
				return collectResults(tasks, results), runErr
			}
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
