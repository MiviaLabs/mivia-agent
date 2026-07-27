// Package subagents provides bounded dependency-aware execution.
package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"sort"
	"sync"
)

type Task struct {
	ID, Name, Owner string
	DependsOn       []string
	Scope           string
	Input           json.RawMessage
	Depth           int
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
	Partial                      bool
}
type Pool struct {
	d *runtime.Dispatcher
	p Policy
}

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
func (p *Pool) Run(ctx context.Context, tasks []Task) ([]Result, error) {
	if len(tasks) > p.p.MaxFanout {
		return nil, fmt.Errorf("fan-out limit exceeded")
	}
	by := map[string]Task{}
	for _, t := range tasks {
		if t.ID == "" || by[t.ID].ID != "" {
			return nil, fmt.Errorf("duplicate task id")
		}
		by[t.ID] = t
	}
	for _, t := range tasks {
		if t.Depth > p.p.MaxDepth {
			return nil, fmt.Errorf("depth limit exceeded")
		}
		for _, dep := range t.DependsOn {
			if _, ok := by[dep]; !ok {
				return nil, fmt.Errorf("missing dependency %q", dep)
			}
		}
	}
	results := make(map[string]Result)
	pending := map[string]Task{}
	for _, t := range tasks {
		pending[t.ID] = t
	}
	var mu sync.Mutex
	for len(pending) > 0 {
		ready := []Task{}
		for _, t := range pending {
			ok := true
			for _, dep := range t.DependsOn {
				if _, done := results[dep]; !done {
					ok = false
				}
			}
			if ok {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("dependency cycle")
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
		sem := make(chan struct{}, p.p.Workers)
		locks := map[string]*sync.Mutex{}
		var locksMu sync.Mutex
		var wg sync.WaitGroup
		for _, t := range ready {
			delete(pending, t.ID)
			t := t
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					mu.Lock()
					results[t.ID] = Result{TaskID: t.ID, Err: ctx.Err(), Status: "canceled"}
					mu.Unlock()
					return
				}
				defer func() { <-sem }()
				if t.Scope != "" {
					locksMu.Lock()
					lock := locks[t.Scope]
					if lock == nil {
						lock = &sync.Mutex{}
						locks[t.Scope] = lock
					}
					locksMu.Unlock()
					lock.Lock()
					defer lock.Unlock()
				}
				r := p.d.Invoke(ctx, runtime.Request{ID: t.ID, ParentID: t.Owner, Name: t.Name, Kind: runtime.Subagent, Scope: t.Scope, Input: t.Input, Depth: t.Depth})
				mu.Lock()
				s := "completed"
				if r.Err != nil {
					s = "failed"
				}
				results[t.ID] = Result{TaskID: t.ID, Output: r.Output, Err: r.Err, Status: s, Provenance: r.Metadata}
				mu.Unlock()
			}()
		}
		wg.Wait()
		if ctx.Err() != nil && !p.p.Partial {
			return nil, ctx.Err()
		}
	}
	out := make([]Result, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, results[t.ID])
	}
	return out, nil
}
