// Package runtime contains the shared invocation boundary for model-directed work.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	mu           sync.Mutex
	handlers     map[Kind]map[string]Handler
	active       map[string]struct{}
	completed    map[string]Result
	waiters      map[string]chan Result
	fingerprints map[string]string
	spent        map[string]int
	resources    map[string]chan struct{}
	closeHooks   []func()
	policy       Policy
}

// Has reports whether a handler is registered for a runtime kind and name.
// It is used to validate that model-visible capabilities are executable.
func (d *Dispatcher) Has(k Kind, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.handlers[k][name]
	return ok
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
	return &Dispatcher{handlers: map[Kind]map[string]Handler{Tool: {}, Skill: {}, Subagent: {}}, active: map[string]struct{}{}, completed: map[string]Result{}, waiters: map[string]chan Result{}, fingerprints: map[string]string{}, spent: map[string]int{}, resources: map[string]chan struct{}{}, policy: policy}
}

// Close releases retained invocation state at the end of a session. Active
// calls are owned by their contexts and must be canceled by the caller first.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	d.completed = map[string]Result{}
	d.fingerprints = map[string]string{}
	d.spent = map[string]int{}
	d.resources = map[string]chan struct{}{}
	hooks := append([]func(){}, d.closeHooks...)
	d.closeHooks = nil
	d.mu.Unlock()
	for _, hook := range hooks {
		hook()
	}
}

// OnClose registers a callback invoked once when Close releases dispatcher
// state. It is used by owners of dispatcher-keyed resources to unregister
// those resources without retaining sessions for the process lifetime.
func (d *Dispatcher) OnClose(hook func()) {
	if hook == nil {
		return
	}
	d.mu.Lock()
	d.closeHooks = append(d.closeHooks, hook)
	d.mu.Unlock()
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
		return d.failResult(req, meta, started, err, nil)
	}
	if err := d.validateRequest(req); err != nil {
		return fail(err)
	}
	h, allowed, dup, waiter, err := d.reserve(req, meta.InputHash)
	if err != nil {
		return fail(err)
	}
	defer func() { d.mu.Lock(); delete(d.active, req.ID); d.mu.Unlock() }()
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
	if h == nil {
		return fail(fmt.Errorf("unknown %s %q", req.Kind, req.Name))
	}
	if !allowed {
		return fail(fmt.Errorf("permission denied for %q", req.Name))
	}
	return d.execute(ctx, req, h, started, meta, fail)
}

func (d *Dispatcher) validateRequest(req Request) error {
	if len(req.Input) > d.policy.MaxInputBytes {
		return fmt.Errorf("input budget exceeded")
	}
	if d.policy.MaxBudget > 0 && req.Budget > d.policy.MaxBudget {
		return fmt.Errorf("budget exceeded")
	}
	if req.Depth > d.policy.MaxDepth {
		return fmt.Errorf("invocation depth exceeded")
	}
	return nil
}

func (d *Dispatcher) reserve(req Request, inputHash string) (Handler, bool, bool, chan Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous, ok := d.fingerprints[req.ID]; ok && previous != inputHash {
		return nil, false, false, nil, fmt.Errorf("invocation id reused with different input")
	}
	if previous, ok := d.completed[req.ID]; ok {
		previous.Metadata.Status = "duplicate"
		return nil, false, true, closedResult(previous), nil
	}
	budgetKey := req.TurnID
	if budgetKey == "" {
		budgetKey = req.ParentID
	}
	if budgetKey == "" {
		budgetKey = req.ID
	}
	if d.policy.MaxBudget > 0 && d.spent[budgetKey]+req.Budget > d.policy.MaxBudget {
		return nil, false, false, nil, fmt.Errorf("cumulative budget exceeded")
	}
	d.spent[budgetKey] += req.Budget
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
		d.fingerprints[req.ID] = inputHash
	}
	return h, allowed, dup, waiter, nil
}

func closedResult(result Result) chan Result {
	waiter := make(chan Result, 1)
	waiter <- result
	return waiter
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
		// Keep tool body (error text / partial stdout) for parent model + UI.
		return d.failResult(req, meta, started, err, out)
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

// failResult builds a failed Result, preserving any tool body so parent agents
// and the TUI receive the failure reason (not only Metadata.RedactedOutput).
func (d *Dispatcher) failResult(req Request, meta Metadata, started time.Time, err error, out []byte) Result {
	meta.Status = "failed"
	if errors.Is(err, context.Canceled) {
		meta.Status = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		meta.Status = "timed_out"
	}
	meta.Duration = time.Since(started)
	meta.RedactedInput = redact(req.Input)
	if len(out) == 0 && err != nil {
		out = []byte("error: " + err.Error())
	}
	if len(out) > 0 {
		meta.RedactedOutput = redact(out)
		meta.OutputHash = hash(out)
	} else if err != nil {
		meta.RedactedOutput = redact([]byte(err.Error()))
	}
	d.emit(meta)
	result := Result{
		ID: req.ID, Name: req.Name, Kind: req.Kind,
		Output: json.RawMessage(append([]byte(nil), out...)),
		Err:    err, Metadata: meta,
	}
	d.mu.Lock()
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
		return truncateText(redactText(string(b)))
	}
	scrub(v)
	x, _ := json.Marshal(v)
	return truncateText(redactText(string(x)))
}

var sensitiveText = regexp.MustCompile(`(?i)(bearer\s+|(?:sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9._~-]+|(?:password|passwd|token|secret|api[_-]?key|authorization|private[_-]?key|ssn|email|phone)\s*[:=]\s*)[^\s,;]+`)
var sensitivePEM = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)`)

func redactText(s string) string {
	s = sensitivePEM.ReplaceAllString(s, "[redacted private key]")
	return sensitiveText.ReplaceAllString(s, "$1[redacted]")
}
func truncateText(s string) string {
	if len(s) > 256 {
		s = s[:256]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func scrub(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			l := strings.ToLower(k)
			if strings.Contains(l, "secret") || strings.Contains(l, "token") || strings.Contains(l, "password") || strings.Contains(l, "authorization") || strings.Contains(l, "private") || strings.Contains(l, "prompt") || strings.Contains(l, "reasoning") || strings.Contains(l, "ssn") || strings.Contains(l, "email") || strings.Contains(l, "phone") {
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
