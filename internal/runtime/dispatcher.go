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
	WorkLimits                    WorkLimits
	DisableProviderReplay         bool
	Permission                    string
	Depth, Retry                  int
	// Step is the loop-stamped model step this invocation belongs to. 0 means
	// legacy turn-scoped dedup; Step > 0 scopes the per-turn dedup to that step,
	// so an identical call re-issued in a LATER step of the same turn re-runs
	// while a same-step re-issue still dedups.
	Step int
	// SkipDedup exempts this Tool invocation from the per-turn dedup and the
	// ID-keyed dedup state (completed map, active/waiters): it never reserves a
	// flight key, never joins a waiter, is never answered from a recorded
	// result, and never writes dedup state. Zero value keeps today's behavior.
	SkipDedup bool
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

// Metadata, Event and the invocation record types live in dispatcher_types.go.
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
	waiters      map[string][]chan Result
	// turnResults dedups identical Tool invocations within one logical turn
	// (same tool+input, fresh call ID); see turn_dedup.go. inFlight tracks
	// identical Tool invocations still executing under their flight key, so a
	// concurrent duplicate waits for the owner's result instead of re-running.
	turnResults  map[string]map[string]Result
	turnOrder    []string
	inFlight     map[string]*inFlightEntry
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
	return &Dispatcher{handlers: map[Kind]map[string]Handler{Tool: {}, Skill: {}, Subagent: {}}, toolCeilings: map[string]int{}, active: map[string]struct{}{}, completed: map[string]Result{}, waiters: map[string][]chan Result{}, fingerprints: map[string]string{}, spent: map[string]int{}, resources: map[string]chan struct{}{}, turnResults: map[string]map[string]Result{}, inFlight: map[string]*inFlightEntry{}, policy: policy}
}

// Close releases retained invocation state at the end of a session. It wakes
// duplicate callers with a closed result. Active owners continue under their
// caller-owned contexts, but cannot restore released dispatcher state.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	closed := Result{Err: errDispatcherClosed, Metadata: Metadata{Status: "closed"}}
	for _, waiters := range d.waiters {
		deliverWaiters(waiters, closed)
	}
	for _, entry := range d.inFlight {
		deliverWaiters(entry.waiters, closed)
	}
	d.active = map[string]struct{}{}
	d.waiters = map[string][]chan Result{}
	d.completed = map[string]Result{}
	d.fingerprints = map[string]string{}
	d.spent = map[string]int{}
	d.resources = map[string]chan struct{}{}
	d.turnResults = map[string]map[string]Result{}
	d.turnOrder = nil
	d.inFlight = map[string]*inFlightEntry{}
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
	if d.isClosed() {
		return dispatcherClosedResult(req)
	}
	fail := func(err error) Result { return d.terminalFailure(req, meta, started, err) }
	if err := d.validateRequest(req); err != nil {
		return d.preReservationFailure(req, meta, started, err)
	}
	if req.ParentID == req.ID {
		return d.preReservationFailure(req, meta, started, fmt.Errorf("duplicate or recursive invocation %q", req.ID))
	}
	res, err := d.reserve(req, meta.InputHash)
	if err != nil {
		return d.preReservationFailure(req, meta, started, err)
	}
	// From here the invocation holds a real reservation. Only the owner (a
	// non-dup reservation) may complete its flight entry or write the dedup
	// bucket; dups return the owner's recorded result instead.
	finish := func(err error) Result {
		r := d.terminalFailure(req, meta, started, err)
		if !res.dup {
			d.recordTurnResult(req, r)
		}
		return r
	}
	// Only the invocation that reserved the ID owns the active marker. A
	// duplicate waiter may cancel while the owner is still running; it must not
	// make the ID look available to a third caller. A SkipDedup call never
	// registered the marker and must not delete it.
	if !res.dup && !req.SkipDedup {
		defer func() { d.mu.Lock(); delete(d.active, req.ID); d.mu.Unlock() }()
	}
	if res.dup {
		return waitDuplicateResult(ctx, req, res.waiter)
	}
	if res.handler == nil {
		return finish(fmt.Errorf("unknown %s %q", req.Kind, req.Name))
	}
	if !res.allowed {
		return finish(fmt.Errorf("permission denied for %q", req.Name))
	}
	// The gate sits after reserve and before execute. A block therefore happens
	// with the budget already charged and the active marker installed; the
	// deferred cleanup above still runs and blockedResult still delivers to
	// waiters, so a blocked call cannot wedge a duplicate waiter.
	verdict := d.preInvoke(ctx, req)
	if verdict.Denied {
		blocked := d.blockedResult(req, meta, started, verdict.Reason)
		blocked.HookRuns = verdict.Runs
		// Resolve any in-flight duplicate waiters with the block, but do NOT
		// record it in the turn bucket: an admission verdict can legitimately
		// change mid-turn and the re-issued call must be re-evaluated.
		d.mu.Lock()
		d.completeTurnInFlight(req, blocked)
		d.mu.Unlock()
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
	d.recordTurnResult(req, result)
	return result
}

// joinHookContext, waitDuplicateResult and closedResult live in
// dispatcher_helpers.go.

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

// turnDedupKey, maxTurnBuckets and recordTurnResult live in turn_dedup.go;
// their contracts are documented there and are not duplicated here.
// acquireScope serializes invocations sharing one scope onto a single in-flight
// slot (one holder at a time). Returns a release function the caller must run.
func (d *Dispatcher) acquireScope(ctx context.Context, scope string) (func(), error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, errDispatcherClosed
	}
	resource := d.resources[scope]
	if resource == nil {
		resource = make(chan struct{}, 1)
		d.resources[scope] = resource
	}
	d.mu.Unlock()
	select {
	case resource <- struct{}{}:
		return func() { <-resource }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Dispatcher) execute(ctx context.Context, req Request, res reservation, started time.Time, meta Metadata, fail func(error) Result) Result {
	// fail is the terminal builder for failures that return BEFORE postInvoke
	// (scope-acquisition failure, runaway output). It must not record the turn
	// bucket: Invoke records every execute-returned result exactly once, after
	// the hooks attach HookContext, so a same-step duplicate is served the same
	// post-hook result as the owner (DC-9 dedup fidelity).
	h := res.handler
	meta.Status = "started"
	d.emit(meta)
	if req.Scope != "" {
		release, err := d.acquireScope(ctx, req.Scope)
		if err != nil {
			return fail(err)
		}
		defer release()
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
	// Record the ID-keyed completed entry only for calls that read it
	// (turnDedupKey == "", see reserve); the ID-keyed waiter delivery inside
	// stays ungated.
	d.completeIDKeyed(req, result)
	d.mu.Unlock()
	return result
}

func ephemeralMarker(h Handler, req Request) string {
	if ephemeral, ok := h.(ephemeralResultHandler); ok {
		return ephemeral.EphemeralResultMarker(req)
	}
	return ""
}
