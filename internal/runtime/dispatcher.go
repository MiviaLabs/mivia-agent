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
	ID, ParentID, TurnID, SessionID, Role, Name, Scope string
	// AgentName, AgentDigest and Skill are immutable work metadata used by
	// agent-routing handlers. They are not policy scope or permission grants.
	AgentName, AgentDigest, Skill string
	Kind                          Kind
	Input                         json.RawMessage
	Timeout                       time.Duration
	Budget                        int
	Permission                    string
	Depth                         int
	Retry                         int
}
type Result struct {
	ID, Name string
	Kind     Kind
	Output   json.RawMessage
	Err      error
	Attempts int
	Metadata Metadata
}

// Metadata is the audit record for one invocation.
//
// InputPreview and OutputPreview are bounded previews of the payloads: at most
// 256 bytes each. They are redacted ONLY to the extent the workspace's
// configured redaction policy removes something; an unconfigured workspace
// gets raw content, so treat them as payload, not as sanitised text. They are
// empty unless a Policy.Sink is attached - with no sink there is no consumer,
// so the previews are not computed at all.
type Metadata struct {
	ID, ParentID, TurnID, Name, Kind, Status, Scope, InputHash, OutputHash string
	Duration                                                               time.Duration
	InputPreview, OutputPreview                                            string
}
type Event struct {
	Type     string
	Metadata Metadata
}
type Handler interface {
	Invoke(context.Context, Request) (json.RawMessage, error)
}

type ephemeralResultHandler interface {
	EphemeralResultMarker(Request) string
}
type Policy struct {
	MaxDepth, MaxRetries, MaxInputBytes, MaxOutputBytes int
	MaxBudget                                           int
	Allow                                               map[Kind]map[string]bool
	Sink                                                func(Event)
}
type Dispatcher struct {
	mu       sync.Mutex
	handlers map[Kind]map[string]Handler
	// toolCeilings holds the per-tool output backstop derived from each tool's
	// OWN declared result budget, keyed by tool name. Written only by register
	// (in the same critical section that installs the handler) and read only by
	// effectiveCeilingLocked, so a handler can never be found without its
	// ceiling. Kind==Tool only; a missing entry means Policy.MaxOutputBytes.
	toolCeilings map[string]int
	active       map[string]struct{}
	completed    map[string]Result
	waiters      map[string]chan Result
	fingerprints map[string]string
	spent        map[string]int
	resources    map[string]chan struct{}
	closeHooks   []func()
	closed       bool
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
		policy.MaxInputBytes = defaultInputAllowance
	}
	if policy.MaxOutputBytes <= 0 {
		policy.MaxOutputBytes = outputCeilingFloor
	}
	return &Dispatcher{handlers: map[Kind]map[string]Handler{Tool: {}, Skill: {}, Subagent: {}}, toolCeilings: map[string]int{}, active: map[string]struct{}{}, completed: map[string]Result{}, waiters: map[string]chan Result{}, fingerprints: map[string]string{}, spent: map[string]int{}, resources: map[string]chan struct{}{}, policy: policy}
}

// Close releases retained invocation state at the end of a session. Active
// calls are owned by their contexts and must be canceled by the caller first.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
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
	if d.closed {
		d.mu.Unlock()
		hook()
		return
	}
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

// Policy returns a shallow copy of the dispatcher's effective policy for a
// derived dispatcher. Allow is deliberately omitted: a derived dispatcher
// rebuilds its allow map from its own registered handlers.
func (d *Dispatcher) Policy() Policy {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.policy
	p.Allow = nil
	return p
}

// Register installs a handler with no derivable result budget of its own: it
// keeps Policy.MaxOutputBytes as its output ceiling. Registry-backed tools go
// through RegisterTool, which additionally records a per-tool ceiling.
func (d *Dispatcher) Register(k Kind, name string, h Handler) error {
	return d.register(k, name, h, 0)
}

// register is the single install path. It writes the handler and, when
// ceiling > 0 and k is Tool, the per-tool output ceiling in ONE critical
// section, so reserve() can never observe a handler without its ceiling.
func (d *Dispatcher) register(k Kind, name string, h Handler, ceiling int) error {
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
	if k == Tool && ceiling > 0 {
		d.toolCeilings[name] = ceiling
	}
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
	res, err := d.reserve(req, meta.InputHash)
	if err != nil {
		return fail(err)
	}
	// Only the invocation that reserved the ID owns the active marker. A
	// duplicate waiter may cancel while the owner is still running; it must not
	// make the ID look available to a third caller.
	if !res.dup {
		defer func() { d.mu.Lock(); delete(d.active, req.ID); d.mu.Unlock() }()
	}
	if res.dup {
		if req.ParentID == req.ID {
			return fail(fmt.Errorf("duplicate or recursive invocation %q", req.ID))
		}
		select {
		case result := <-res.waiter:
			result.Metadata.Status = "duplicate"
			return result
		case <-ctx.Done():
			return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: ctx.Err(), Metadata: Metadata{ID: req.ID, Name: req.Name, Kind: string(req.Kind), Status: "canceled"}}
		}
	}
	if res.handler == nil {
		return fail(fmt.Errorf("unknown %s %q", req.Kind, req.Name))
	}
	if !res.allowed {
		return fail(fmt.Errorf("permission denied for %q", req.Name))
	}
	return d.execute(ctx, req, res, started, meta, fail)
}

func (d *Dispatcher) validateRequest(req Request) error {
	if req.Budget < 0 {
		return fmt.Errorf("budget must be non-negative")
	}
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

// reservation is what one pass through reserve() resolved for a request. The
// ceiling travels with the handler on purpose: both are read in the same
// critical section, so an invocation can never execute a handler under a bound
// that belongs to some other registration.
type reservation struct {
	handler Handler
	ceiling int
	allowed bool
	dup     bool
	waiter  chan Result
}

func (d *Dispatcher) reserve(req Request, inputHash string) (reservation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous, ok := d.fingerprints[req.ID]; ok && previous != inputHash {
		return reservation{}, fmt.Errorf("invocation id reused with different input")
	}
	if previous, ok := d.completed[req.ID]; ok {
		previous.Metadata.Status = "duplicate"
		return reservation{dup: true, waiter: closedResult(previous)}, nil
	}
	budgetKey := req.TurnID
	if budgetKey == "" {
		budgetKey = req.ParentID
	}
	if budgetKey == "" {
		budgetKey = req.ID
	}
	if d.policy.MaxBudget > 0 && d.spent[budgetKey]+req.Budget > d.policy.MaxBudget {
		return reservation{}, fmt.Errorf("cumulative budget exceeded")
	}
	d.spent[budgetKey] += req.Budget
	res := reservation{
		handler: d.handlers[req.Kind][req.Name],
		ceiling: d.effectiveCeilingLocked(req.Kind, req.Name),
	}
	if names, ok := d.policy.Allow[req.Kind]; ok {
		res.allowed = names[req.Name]
	}
	_, res.dup = d.active[req.ID]
	res.waiter = d.waiters[req.ID]
	if res.handler != nil && !res.dup {
		d.active[req.ID] = struct{}{}
		d.waiters[req.ID] = make(chan Result, 1)
		d.fingerprints[req.ID] = inputHash
	}
	return res, nil
}

func closedResult(result Result) chan Result {
	waiter := make(chan Result, 1)
	waiter <- result
	return waiter
}
func (d *Dispatcher) execute(ctx context.Context, req Request, res reservation, started time.Time, meta Metadata, fail func(error) Result) Result {
	h := res.handler
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
	callCtx = ContextWithCaller(callCtx, Caller{
		SessionID: req.SessionID,
		TurnID:    req.TurnID,
		ParentID:  req.ParentID,
		Depth:     req.Depth,
		Role:      req.Role,
	})
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
		return d.failResult(req, meta, started, err, out)
	}
	if len(out) > res.ceiling {
		return fail(overCeilingError(req, len(out), res.ceiling))
	}
	meta.Status = "completed"
	meta.OutputHash = hash(out)
	meta.Duration = time.Since(started)
	meta.InputPreview = d.previewFor(req.Input)
	if marker := ephemeralMarker(h, req); marker != "" {
		meta.OutputPreview = marker
	} else {
		meta.OutputPreview = d.previewFor(out)
	}
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

// failResult builds a failed Result. The payload carries a bounded status and
// nothing else; raw provider/tool/error bodies stay out of the result.
func (d *Dispatcher) failResult(req Request, meta Metadata, started time.Time, err error, out []byte) Result {
	meta.Status = "failed"
	if errors.Is(err, context.Canceled) {
		meta.Status = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		meta.Status = "timed_out"
	}
	meta.Duration = time.Since(started)
	meta.InputPreview = d.previewFor(req.Input)
	if len(out) > 0 {
		meta.OutputPreview = d.previewFor(out)
		meta.OutputHash = hash(out)
	} else if err != nil {
		meta.OutputPreview = d.previewFor([]byte(err.Error()))
	}
	d.emit(meta)
	// No content reference is emitted here. This layer has no repository, so
	// nothing stores the error or output bytes under any key, and a reference
	// whose bytes nothing holds is worse than none: it hands the model a pointer
	// that cannot resolve, so ledger_read answers not_found for a reason that has
	// nothing to do with the bytes being absent (INV-AG-10: a reference handed to
	// the model resolves, or it is not handed to the model). The bounded
	// correlation value stays in the audit metadata above - meta.OutputHash for a
	// handler that produced bytes, plus meta.OutputPreview - which is emitted to
	// the sink and never shown to the model.
	//
	// The payload carries the full, unredacted error reason alongside the
	// status. Opaquing failures into a bare {"status":"failed"} left the model
	// unable to distinguish a bad path from a broken tool - every failure looked
	// identical and the only recourse was blind retry (see the write_file
	// debugging session that motivated this). The raw err.Error() is safe to
	// surface here because it originates in mivia's own tool/handler code, which
	// is already required by rule 10 to keep secrets out of error messages; the
	// sink-side audit preview (OutputPreview) already handles redaction for
	// operator-facing logs. The model needs the same fidelity to debug itself.
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	payload := map[string]string{
		"status": meta.Status,
		"error":  reason,
	}
	safeOutput, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		safeOutput = []byte(`{"status":"failed"}`)
	}
	result := Result{
		ID: req.ID, Name: req.Name, Kind: req.Kind,
		Output: json.RawMessage(safeOutput),
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

func ephemeralMarker(h Handler, req Request) string {
	if ephemeral, ok := h.(ephemeralResultHandler); ok {
		return ephemeral.EphemeralResultMarker(req)
	}
	return ""
}
