// Package runtime contains the shared invocation boundary for model-directed work.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	Tool     Kind = "tool"
	Skill    Kind = "skill"
	Subagent Kind = "subagent"
)

type Request struct {
	ID, ParentID, TurnID, Name, Scope string
	Kind                              Kind
	Input                             json.RawMessage
	Timeout                           time.Duration
	Budget                            int
	Permission                        string
	Depth                             int
	Retry                             int
}
type Result struct {
	ID, Name string
	Kind     Kind
	Output   json.RawMessage
	Err      error
	Attempts int
	Metadata Metadata
}
type Metadata struct {
	ID, ParentID, TurnID, Name, Kind, Status, Scope, InputHash, OutputHash string
	Duration                                                               time.Duration
	RedactedInput, RedactedOutput                                          string
}
type Event struct {
	Type     string
	Metadata Metadata
}
type Handler interface {
	Invoke(context.Context, Request) (json.RawMessage, error)
}
type Policy struct {
	MaxDepth, MaxRetries, MaxInputBytes, MaxOutputBytes int
	MaxBudget                                           int
	Allow                                               map[Kind]map[string]bool
	Sink                                                func(Event)
}
type Dispatcher struct {
	mu        sync.Mutex
	handlers  map[Kind]map[string]Handler
	active    map[string]struct{}
	completed map[string]Result
	waiters   map[string]chan Result
	resources map[string]chan struct{}
	policy    Policy
}

func New(policy Policy) *Dispatcher {
	if policy.MaxDepth <= 0 {
		policy.MaxDepth = 8
	}
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	if policy.MaxInputBytes <= 0 {
		policy.MaxInputBytes = 64 << 10
	}
	if policy.MaxOutputBytes <= 0 {
		policy.MaxOutputBytes = 256 << 10
	}
	return &Dispatcher{handlers: map[Kind]map[string]Handler{Tool: {}, Skill: {}, Subagent: {}}, active: map[string]struct{}{}, completed: map[string]Result{}, waiters: map[string]chan Result{}, resources: map[string]chan struct{}{}, policy: policy}
}
func (d *Dispatcher) Allow(k Kind, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.policy.Allow == nil {
		d.policy.Allow = map[Kind]map[string]bool{}
	}
	if d.policy.Allow[k] == nil {
		d.policy.Allow[k] = map[string]bool{}
	}
	d.policy.Allow[k][name] = true
}
func (d *Dispatcher) Register(k Kind, name string, h Handler) error {
	if h == nil || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid handler")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.handlers[k]; !ok {
		return fmt.Errorf("invalid kind %q", k)
	}
	if _, ok := d.handlers[k][name]; ok {
		return fmt.Errorf("duplicate handler %q", name)
	}
	d.handlers[k][name] = h
	if d.policy.Allow == nil {
		d.policy.Allow = map[Kind]map[string]bool{}
	}
	if d.policy.Allow[k] == nil {
		d.policy.Allow[k] = map[string]bool{}
	}
	d.policy.Allow[k][name] = true
	return nil
}
func (d *Dispatcher) Invoke(ctx context.Context, req Request) Result {
	started := time.Now()
	if req.ID == "" {
		sum := sha256.Sum256(append([]byte(req.Name), req.Input...))
		req.ID = hex.EncodeToString(sum[:8])
	}
	meta := Metadata{ID: req.ID, ParentID: req.ParentID, TurnID: req.TurnID, Name: req.Name, Kind: string(req.Kind), Scope: req.Scope, InputHash: hash(req.Input)}
	fail := func(err error) Result {
		meta.Status = "failed"
		if errors.Is(err, context.Canceled) {
			meta.Status = "canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			meta.Status = "timed_out"
		}
		meta.Duration = time.Since(started)
		d.emit(meta)
		result := Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: err, Metadata: meta}
		d.mu.Lock()
		if waiter := d.waiters[req.ID]; waiter != nil {
			delete(d.waiters, req.ID)
			waiter <- result
		}
		d.mu.Unlock()
		return result
	}
	if len(req.Input) > d.policy.MaxInputBytes {
		return fail(fmt.Errorf("input budget exceeded"))
	}
	if d.policy.MaxBudget > 0 && req.Budget > d.policy.MaxBudget {
		return fail(fmt.Errorf("budget exceeded"))
	}
	if req.Depth > d.policy.MaxDepth {
		return fail(fmt.Errorf("invocation depth exceeded"))
	}
	d.mu.Lock()
	if previous, ok := d.completed[req.ID]; ok {
		d.mu.Unlock()
		previous.Metadata.Status = "duplicate"
		return previous
	}
	h := d.handlers[req.Kind][req.Name]
	allowed := false
	if names, ok := d.policy.Allow[req.Kind]; ok {
		allowed = names[req.Name]
	}
	_, dup := d.active[req.ID]
	waiter := d.waiters[req.ID]
	if h != nil && !dup {
		d.active[req.ID] = struct{}{}
		d.waiters[req.ID] = make(chan Result, 1)
	}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.active, req.ID); d.mu.Unlock() }()
	if h == nil {
		return fail(fmt.Errorf("unknown %s %q", req.Kind, req.Name))
	}
	if !allowed {
		return fail(fmt.Errorf("permission denied for %q", req.Name))
	}
	if dup {
		if req.ParentID == req.ID {
			return fail(fmt.Errorf("duplicate or recursive invocation %q", req.ID))
		}
		select {
		case result := <-waiter:
			result.Metadata.Status = "duplicate"
			return result
		case <-ctx.Done():
			return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: ctx.Err(), Metadata: Metadata{ID: req.ID, Name: req.Name, Kind: string(req.Kind), Status: "canceled"}}
		}
	}
	return d.execute(ctx, req, h, started, meta, fail)
}
func (d *Dispatcher) execute(ctx context.Context, req Request, h Handler, started time.Time, meta Metadata, fail func(error) Result) Result {
	meta.Status = "started"
	d.emit(meta)
	if req.Scope != "" {
		d.mu.Lock()
		resource := d.resources[req.Scope]
		if resource == nil {
			resource = make(chan struct{}, 1)
			d.resources[req.Scope] = resource
		}
		d.mu.Unlock()
		select {
		case resource <- struct{}{}:
		case <-ctx.Done():
			return fail(ctx.Err())
		}
		defer func() { <-resource }()
	}
	callCtx, cancel := ctx, func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	maxAttempts := req.Retry + 1
	if maxAttempts > d.policy.MaxRetries+1 {
		maxAttempts = d.policy.MaxRetries + 1
	}
	attempts := 0
	var out []byte
	var err error
	for i := 0; i < maxAttempts; i++ {
		attempts++
		out, err = h.Invoke(callCtx, req)
		if err == nil {
			break
		}
		if callCtx.Err() != nil {
			break
		}
		if i+1 < maxAttempts {
			meta.Status = "retrying"
			d.emit(meta)
		}
	}
	if err != nil {
		return fail(err)
	}
	if len(out) > d.policy.MaxOutputBytes {
		return fail(fmt.Errorf("output budget exceeded"))
	}
	meta.Status = "completed"
	meta.OutputHash = hash(out)
	meta.Duration = time.Since(started)
	meta.RedactedInput = redact(req.Input)
	meta.RedactedOutput = redact(out)
	d.emit(meta)
	result := Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Output: out, Attempts: attempts, Metadata: meta}
	d.mu.Lock()
	d.completed[req.ID] = result
	if waiter := d.waiters[req.ID]; waiter != nil {
		delete(d.waiters, req.ID)
		waiter <- result
	}
	d.mu.Unlock()
	return result
}
func (d *Dispatcher) emit(m Metadata) {
	if d.policy.Sink != nil {
		d.policy.Sink(Event{Type: m.Status, Metadata: m})
	}
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func redact(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return "[redacted]"
	}
	scrub(v)
	x, _ := json.Marshal(v)
	if len(x) > 256 {
		x = x[:256]
	}
	return string(x)
}
func scrub(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			l := strings.ToLower(k)
			if strings.Contains(l, "secret") || strings.Contains(l, "token") || strings.Contains(l, "password") || strings.Contains(l, "authorization") {
				x[k] = "[redacted]"
			} else {
				scrub(val)
			}
		}
	case []any:
		for _, val := range x {
			scrub(val)
		}
	}
}
