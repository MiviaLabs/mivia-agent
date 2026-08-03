// Package runtime contains the shared invocation boundary for model-directed work.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// AgentName, AgentDigest, Skill, ProviderName and Model are immutable work metadata used by
	// agent-routing handlers. They are not policy scope or permission grants.
	AgentName, AgentDigest, Skill string
	ProviderName, Model           string
	Kind                          Kind
	Input                         json.RawMessage
	Timeout                       time.Duration
	Budget                        int
	Permission                    string
	Depth, Retry                  int
	// OutputSchema: structured subagent output schema (tools/02); nil = free-text.
	OutputSchema map[string]any
}
type Result struct {
	ID, Name string
	Kind     Kind
	Output   json.RawMessage
	Err      error
	Attempts int
	Metadata Metadata
	// HookContext is advisory text a PostToolUse hook produced for this
	// invocation. It travels in its own separately bounded field and is NEVER
	// spliced into Output: appending it there would write past the per-tool
	// ceiling check inside execute (INV-AG-25/26/27) and leave Metadata's
	// OutputHash and OutputPreview describing bytes the model never received.
	HookContext string
	// HookRuns records the lifecycle hooks that executed for this invocation,
	// for the OPERATOR's view. It is not the model's copy and not the audit
	// record: a hook that ran and said nothing appears here and nowhere else,
	// which is the whole reason it exists.
	HookRuns []HookRun
}

// HookRun is one lifecycle hook execution, described for display.
//
// runtime deliberately does not import internal/hooks, so this is a plain
// value the wiring layer fills in rather than a re-export.
type HookRun struct {
	// Event is PreToolUse, PostToolUse or Stop.
	Event string
	// Program is the hook script's name, not its path: this reaches a screen,
	// and the absolute path runs through the operator's home directory.
	Program string
	// Denied is true for the PreToolUse run that blocked the call.
	Denied bool
	// Output is what this hook said - advisory text, or the block reason.
	// Empty means it ran silently, which is normal and still worth showing.
	Output string
	// Warning is the operator diagnostic this run produced, if it misbehaved.
	Warning string
}

// HookResult is a reactive hook's answer: what the model is told, and what the
// operator is shown. They are separate fields because they are separate
// questions - a hook that ran silently has a run to report and nothing to say.
type HookResult struct {
	Context string
	Runs    []HookRun
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
	// PreInvokeHook and PostInvokeHook are the optional lifecycle gates. They
	// live on Policy next to Sink, and that placement is load-bearing rather
	// than incidental: Policy is copied to derived dispatchers by
	// Dispatcher.Policy(), which clears only Allow, so the hooks propagate to
	// scoped subagent dispatchers. A PreToolUse gate a subagent escapes is not
	// a gate - subagents run the same tools against the same workspace.
	//
	// internal/runtime deliberately does not import internal/hooks: these are
	// plain func fields, so nil is no hooks, one nil compare, and today's
	// behaviour exactly.
	PreInvokeHook  func(context.Context, Request) HookVerdict
	PostInvokeHook func(context.Context, Request, Result) HookResult
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
	// The gate sits after reserve and before execute. A block therefore happens
	// with the budget already charged and the active marker installed; the
	// deferred cleanup above still runs and blockedResult still delivers to
	// waiters, so a blocked call cannot wedge a duplicate waiter.
	verdict := d.preInvoke(ctx, req)
	if verdict.Denied {
		blocked := d.blockedResult(req, meta, started, verdict.Reason)
		blocked.HookRuns = verdict.Runs
		return blocked
	}
	result := d.execute(ctx, req, res, started, meta, fail)
	// An allowing PreToolUse hook can still have advisory text - that is what
	// additionalContext is for - and it is merged with the reactive event's
	// rather than replaced by it. Reading the verdict's Context and then
	// discarding it is what this layer used to do, which made the field a
	// documented feature that reached nothing.
	post := d.postInvoke(ctx, req, result)
	result.HookContext = boundHookContext(joinHookContext(verdict.Context, post.Context))
	result.HookRuns = append(verdict.Runs, post.Runs...)
	return result
}

// joinHookContext keeps both events' advice, separated. Neither event may
// silence the other: a PreToolUse note about the workspace and a PostToolUse
// formatter's report are about different things.
func joinHookContext(pre, post string) string {
	switch {
	case pre == "":
		return post
	case post == "":
		return pre
	default:
		return pre + "\n" + post
	}
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
	out, ceilErr := applyOutputCeiling(out, res.ceiling, req)
	if ceilErr != nil {
		return fail(ceilErr)
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

func ephemeralMarker(h Handler, req Request) string {
	if ephemeral, ok := h.(ephemeralResultHandler); ok {
		return ephemeral.EphemeralResultMarker(req)
	}
	return ""
}
