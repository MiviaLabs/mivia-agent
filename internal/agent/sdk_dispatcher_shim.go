// Package agent - dispatcher+cap tool shim for the SDK backend.
//
// The legacy loop executes every tool call through
// Options.Dispatcher.Invoke (loop_tool_exec.go:40-47), which fires the
// parent's PreInvokeHook gate, the PostInvokeHook advisory, the dedup
// cache, and the policy checks, and then post-processes the outcome in
// buildExecResult: the MaxToolResultChars / capability cap, the
// remainder spool, and the hook-context attach. The SDK loop instead
// invokes the converted tool directly, which silently drops all of
// that. The shim here is the host-side substitute: each converted SDK
// tool is wrapped so its Run routes through the CLI dispatcher (or the
// raw CLI tool when no dispatcher is wired) and the result body gets
// the same cap/spool/hook-context treatment buildExecResult applies.
//
// It is applied INNERMOST (before the ref-only shim and the turn
// shaping wrapper) so later shapers see the same capped body the
// legacy batch shaper sees.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// sdkTurnState is the per-run state shared by the SDK path's
// completer wrapper and tool shims, and the SINGLE place bridge and
// adapter errors surface (they are recorded here, never returned
// ad hoc from inside per-call hooks that have no error channel).
// steps counts completed Completer calls (the SDK loop's iteration
// counter as the host observes it) so tool dispatches can stamp
// runtime.Request.Step and re-issue an identical read in a later
// iteration without the turn-scoped dedup suppressing it. pass1
// carries the newest pass-1 result parts from the dispatcher shim to
// the turn shaping wrapper so a budget degrade reports the ORIGINAL
// body's true total and pages the original bytes, exactly like the
// legacy shapeBatch chain. dispatcher and spool hold the CURRENT
// surface rotation values: the CLI Surface hook can swap either
// mid-run (agentloop_adapter.go's surface bridge), and the shims
// read them per call instead of the wrap-time Options copy. shape
// owns the turn-level shaping counter so a surface rotation that
// rebuilds the registry keeps charging ONE shared budget. bridgeErr
// records the first surface-bridge failure so the run can fail with
// it after RunSteerable returns (the SDK Surface hook has no error
// channel; a nil return keeps the prior surface).
type sdkTurnState struct {
	steps      atomic.Int64
	pass1      pass1Map
	dispatcher atomic.Pointer[runtime.Dispatcher]
	spool      atomic.Pointer[remainder.Spool]
	// streamTee holds the teeWriter installed as the SDK run's
	// StreamingWriter, so RunAgentLoopOnce can feed it to
	// recordInterruptedPartial when a canceled run leaves streamed
	// bytes that the SDK's hard-fail Result never carried.
	streamTee atomic.Pointer[teeWriter]
	// advertised holds the run's pinned advertised ToolSpec snapshot:
	// the request-0 seed from Options.AdvertisedToolSpecs, replaced by
	// each surface rotation's non-nil ToolSpecs (the legacy keep-rule:
	// nil keeps the prior snapshot). The completer reads it per Chat
	// call and REPLACES the wire request's registry-derived tools with
	// it, so deferred tools outside the registry reach the wire from
	// request 0. See sdk_advertised.go's applyAdvertisedTools for the
	// recovery-request safety note.
	advertised atomic.Pointer[[]provider.ToolSpec]
	shapeOnce  sync.Once
	shape      *turnShapeCounter
	errMu      sync.Mutex
	bridgeErr  error
	// Tool-event synthesis state (sdk_tool_events.go): outcomes keyed
	// by tool call ID, and the once-per-iteration stream-revoke
	// gate (streamRevoked, armed at the first tool call, reset by the
	// EventIterationStart bus subscription).
	toolMu        sync.Mutex
	toolOutcomes  map[string]*toolCallOutcome
	streamRevoked atomic.Bool
}

func newSDKTurnState() *sdkTurnState { return &sdkTurnState{} }

// seedSurface installs the run's initial dispatcher and spool. It
// runs once from the single sdkTurnState construction site
// (buildAgentLoopOptions) so every later read starts from the
// caller's Options values, not zero.
func (s *sdkTurnState) seedSurface(dispatcher *runtime.Dispatcher, spool *remainder.Spool) {
	s.rotateSurface(dispatcher, spool)
}

// rotateSurface installs a surface rotation's dispatcher and spool.
// Nil values keep the current one, mirroring the legacy Surface
// contract's zero-field-means-keep rule.
func (s *sdkTurnState) rotateSurface(dispatcher *runtime.Dispatcher, spool *remainder.Spool) {
	if dispatcher != nil {
		s.dispatcher.Store(dispatcher)
	}
	if spool != nil {
		s.spool.Store(spool)
	}
}

// currentDispatcher returns the live dispatcher, or nil when no
// rotation and no seed ever installed one.
func (s *sdkTurnState) currentDispatcher() *runtime.Dispatcher { return s.dispatcher.Load() }

// currentSpool returns the live remainder spool, or nil when no
// rotation and no seed ever installed one.
func (s *sdkTurnState) currentSpool() *remainder.Spool { return s.spool.Load() }

// setStreamTee installs the run's StreamingWriter tee. It runs once
// from the single sdkTurnState construction site
// (buildAgentLoopOptions); a run without a FinalWriter never installs
// one and currentStreamTee stays nil.
func (s *sdkTurnState) setStreamTee(t *teeWriter) { s.streamTee.Store(t) }

// currentStreamTee returns the run's StreamingWriter tee, or nil when
// the run streamed nowhere.
func (s *sdkTurnState) currentStreamTee() *teeWriter { return s.streamTee.Load() }

// setAdvertised installs the advertised ToolSpec snapshot. A nil
// argument keeps the prior snapshot, mirroring the legacy Surface
// contract's nil-means-keep rule; a non-nil slice (empty included)
// replaces it.
func (s *sdkTurnState) setAdvertised(specs []provider.ToolSpec) {
	if specs == nil {
		return
	}
	s.advertised.Store(&specs)
}

// currentAdvertised returns the live advertised snapshot, or nil when
// neither a seed nor a rotation installed one.
func (s *sdkTurnState) currentAdvertised() []provider.ToolSpec {
	if p := s.advertised.Load(); p != nil {
		return *p
	}
	return nil
}

// shapeCounter lazily builds the one turn-level shaping counter a
// run (and every surface rotation inside it) shares.
func (s *sdkTurnState) shapeCounter() *turnShapeCounter {
	s.shapeOnce.Do(func() { s.shape = newTurnShapeCounter() })
	return s.shape
}

// resetIterationShaping resets nextIndex at the top of each iteration.
// A new broadcast channel is installed so the previous one - which the
// now-completed waiters drained - does not leak as a permanently-open
// pipe. The new channel reads as zero (open) for fresh waiters in
// the new iteration. The close of the previous channel must NOT
// re-close an already-closed one: abortIterationShaping can race this
// path during teardown and has already closed `old`; closing twice
// panics. The aborted flag tells us which side owns the channel's
// closure; only close when we still own it.
func (s *sdkTurnState) resetIterationShaping() {
	if s.shape != nil {
		s.shape.mu.Lock()
		s.shape.nextIndex = 0
		s.shape.aborted = false
		old := s.shape.signal
		s.shape.signal = make(chan struct{})
		owned := !s.shape.closedByAbort
		s.shape.closedByAbort = false
		s.shape.mu.Unlock()
		if owned {
			close(old)
		}
	}
}

// abortIterationShaping wakes any waiters when a batch aborts.
func (s *sdkTurnState) abortIterationShaping() {
	if s.shape != nil {
		s.shape.abort()
	}
}

// recordBridgeError keeps the FIRST bridge error; later ones are
// dropped because the first is the cause the operator needs.
func (s *sdkTurnState) recordBridgeError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.bridgeErr == nil {
		s.bridgeErr = err
	}
}

// bridgeError returns the recorded bridge error, if any.
func (s *sdkTurnState) bridgeError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.bridgeErr
}

// currentStep is the 1-based iteration the in-flight tool batch
// belongs to: the completer bumps steps at the top of each Chat, so a
// batch spawned by iteration N's response sees N.
func (s *sdkTurnState) currentStep() int { return int(s.steps.Load()) }

// pass1Map hands pass-1 resultParts from the dispatcher shim
// (innermost) to the turn shaping wrapper (outermost), keyed by tool
// call ID so parallel calls under MaxConcurrentTools > 1 do not race.
type pass1Map struct {
	mu    sync.Mutex
	parts map[string]resultParts
}

// dispatcherShim wraps one SDK-converted tool with the legacy tool
// execution contract. A nil dispatcher falls back to the raw CLI tool
// (the sdkToolAdapter behavior) but keeps the cap and spool steps.
type dispatcherShim struct {
	inner  sdktools.Tool
	schema sdktools.SchemaTool
	cli    tools.Tool
	opts   Options
	turn   *sdkTurnState
}

var _ sdktools.Tool = (*dispatcherShim)(nil)
var _ sdktools.SchemaTool = (*dispatcherShim)(nil)

func (d *dispatcherShim) Name() string { return d.inner.Name() }

// ParameterSchema and DecodeArguments delegate to the inner tool: the
// SDK's Definitions skips tools that do not implement SchemaTool, so
// without delegation a wrapped tool would vanish from the offered set.
func (d *dispatcherShim) ParameterSchema() []byte { return d.schema.ParameterSchema() }

func (d *dispatcherShim) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return d.schema.DecodeArguments(raw)
}

// armDispatcherTimeout resolves the per-call timeout like
// prepareToolTasks (capability timeout, else Options default, a larger
// model request clamped to the run deadline) and narrows ctx under it.
// The clock starts here because the SDK runs calls one at a time.
//
// resolveToolCallTimeout always returns a positive budget (it floors on
// DefaultToolTimeout), so callTimeout starts strictly positive. A
// model-requested extension only ever raises it, and clampToDeadline
// only ever tightens that raise to the parent ctx's remaining time - but
// when the parent deadline has already passed, or is effectively now,
// clampToDeadline's result can be <= 0. That must NOT disable the
// per-call bound entirely (leaving ctx unnarrowed lets a stuck syscall
// hang forever, past every turn deadline - the exact failure the walk
// goroutine race in walkFilteredFiles exists to escape, and it has
// nothing to race against if ctx never gets a deadline in the first
// place). Falling back to the original resolved budget instead keeps
// every tool call bounded no matter what the request or the parent
// deadline look like.
func armDispatcherTimeout(ctx context.Context, opts Options, args []byte, capability tools.Capability) (context.Context, context.CancelFunc, time.Duration) {
	callTimeout := resolveToolCallTimeout(opts.ToolTimeout, capability.Timeout)
	if requested := requestedToolTimeout(args); requested > callTimeout {
		if clamped := clampToDeadline(ctx, requested); clamped > 0 {
			callTimeout = clamped
		}
	}
	narrowed, cancel := context.WithTimeout(ctx, callTimeout)
	return narrowed, cancel, callTimeout
}

// Run executes one tool call the way the legacy loop does: through the
// dispatcher when one is wired (hooks, gate, dedup, policy), under the
// Options.ToolTimeout ceiling, then capped and spooled like
// buildExecResult. The model-visible body is always a string - an
// errored call surfaces as "error: <reason>" content, never as a hard
// run failure, so the model can react and continue.
func (d *dispatcherShim) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	args, err := json.Marshal(in.Value)
	if err != nil {
		return sdktools.Out{}, fmt.Errorf("agent: tool %q: marshal arguments: %w", d.inner.Name(), err)
	}
	capability := tools.Capability{}
	if capable, ok := d.cli.(tools.CapableTool); ok {
		capability = capable.Capability(args)
	}
	ctx, cancelTimeout, callTimeout := armDispatcherTimeout(ctx, d.opts, args, capability)
	defer cancelTimeout()
	var result, hookContext string
	dispatcher, spool := d.dispatcherAndSpool()
	callKey := toolCallKeyFromContext(ctx, d.inner.Name())
	// Legacy "running" tool_start: the analogue of loop_tool_exec.go's
	// pre-dispatch emission, keyed on the call ID in context.
	if callKey != "" {
		emit(d.opts, Event{Kind: EventToolStart, ToolCallID: callKey, Name: d.inner.Name(), Detail: "running"})
	}
	r := dispatcher.Invoke(ctx, runtime.Request{
		ParentID: d.opts.ParentID, TurnID: d.opts.TurnID, SessionID: d.opts.SessionID,
		Role: d.opts.Role, Depth: d.opts.Depth, Budget: d.opts.Budget,
		Kind: runtime.Tool, Name: d.inner.Name(), Input: args, Timeout: callTimeout,
		Step: d.turn.currentStep(), SkipDedup: !capability.Dedups(),
	})
	// A duplicate never re-ran: the model's result is the suppression
	// notice, but the OWNER's pre-rewrite body is what failed-duplicate
	// detection must scan (a run_command duplicate reports its non-zero
	// child exit in the recorded header with err=nil; toolResultBodyFailed
	// in loop_tools.go operates on the originalBody for exactly this
	// reason). Capture it BEFORE the notice rewrite.
	originalBody := string(r.Output)
	result = originalBody
	if r.IsDuplicate() {
		result = duplicateDeliveryNotice
	} else if r.Err != nil && strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("error: %v", r.Err)
	}
	hookContext = r.HookContext
	// D10 mirror: an ephemeral body is capped with a NIL spool so the
	// notice never mints a ref the scrub exists to remove.
	_, ephemeral := d.cli.(tools.EphemeralResultTool)
	if ephemeral {
		spool = nil
	}
	capBytes := effectiveResultCap(d.opts.MaxToolResultChars, capability.MaxResultBytes)
	capped, refA, truncated := remainder.CapWithSpoolRef(spool, d.opts.SessionID, result, capBytes)
	if d.turn != nil && callKey != "" {
		d.turn.pass1.store(callKey, resultParts{
			cappedBody:   capped,
			refA:         refA,
			totalN:       len(result),
			effectiveCap: capBytes,
			hookContext:  hookContext,
			truncated:    truncated,
			ephemeral:    ephemeral,
			toolName:     d.inner.Name(),
		})
	}
	body := appendHookContext(capped, hookContext)
	d.recordToolEventOutcome(callKey, args, body, r.Err, ephemeral, r.IsDuplicate(), originalBody)
	return sdktools.Out{Value: body}, nil
}

// dispatcherAndSpool reads the live dispatcher and spool from the turn
// state when available, falling back to the wrap-time Options copies.
func (d *dispatcherShim) dispatcherAndSpool() (*runtime.Dispatcher, *remainder.Spool) {
	dispatcher := d.opts.Dispatcher
	spool := d.opts.RemainderSpool
	if d.turn != nil {
		if live := d.turn.currentDispatcher(); live != nil {
			dispatcher = live
		}
		if live := d.turn.currentSpool(); live != nil {
			spool = live
		}
	}
	return dispatcher, spool
}

// toolCallKeyFromContext returns the lookup key for the in-flight tool call:
// call.ID when non-empty, falling back to call.Name for ID-less test fixtures.
func toolCallKeyFromContext(ctx context.Context, fallbackName string) string {
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		if tc.ID != "" {
			return tc.ID
		}
		if tc.Name != "" {
			return tc.Name
		}
	}
	return fallbackName
}

// recordToolEventOutcome records the outcome the EventToolCallEnd
// handler renders the legacy tool_end from (sdk_tool_events.go): the
// final model-visible body, the dispatcher's failure flag, and the
// duplicate/originalBody pair that drives the legacy
// "(duplicate)" vocabulary on tool_end (loop_tools.go toolEndDetail).
// An ephemeral tool's marker overrides only the operator preview
// (loop_tools.go emitToolEnd's rule); a later shim (ref-only notice,
// turn-shaping re-cut) that rewrites the body overwrites the record so
// tool_end matches the post-shaping body.
func (d *dispatcherShim) recordToolEventOutcome(callID string, args []byte, body string, dispatchErr error, ephemeral bool, isDuplicate bool, originalBody string) {
	if d.turn == nil || callID == "" {
		return
	}
	preview := ""
	if ephemeral {
		if et, ok := d.cli.(tools.EphemeralResultTool); ok {
			preview = et.EphemeralResultMarker(args)
		}
	}
	d.turn.recordToolOutcomeWithPreview(callID, d.inner.Name(), body, dispatchErr != nil, preview, isDuplicate, originalBody)
}

// store records the pass-1 parts for callID.
func (m *pass1Map) store(callID string, p resultParts) {
	if callID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parts == nil {
		m.parts = make(map[string]resultParts)
	}
	m.parts[callID] = p
}

// take returns the stored parts for callID when body is exactly the
// parts' model-visible output (hook context appended), clearing the
// record. A body rewritten by an intermediate shim (the ref-only
// notice) misses and leaves the caller on its default single-pass path.
//
// The miss path ALWAYS deletes the stored entry: the dispatcher shim
// already ran for this call, so the entry can never again serve a
// later shaping pass (no other wrapper will see the original body
// either). Leaving it orphaned grows the map monotonically across a
// long session and balloons memory under MaxConcurrentTools > 1.
func (m *pass1Map) take(callID string, body string) (resultParts, bool) {
	if callID == "" {
		return resultParts{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parts == nil {
		return resultParts{}, false
	}
	p, ok := m.parts[callID]
	if !ok {
		return resultParts{}, false
	}
	delete(m.parts, callID)
	if p.cappedBody == "" {
		return resultParts{}, false
	}
	if body != appendHookContext(p.cappedBody, p.hookContext) {
		return resultParts{}, false
	}
	return p, true
}

// purge removes any stored pass-1 entry for callID. Callers that
// observe a mismatch via take should follow up with purge so the
// entry does not leak; the take helper already deletes on miss, but
// purge is the public seam for callers that never called take at all
// (e.g. an aborted or skipped call). Safe on a missing key.
func (m *pass1Map) purge(callID string) {
	if callID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.parts, callID)
}

// applyDispatcherShim wraps every tool in the converted SDK registry
// with the dispatcher shim. It is a no-op when no dispatcher is wired
// and no result cap is configured, so callers that never set either
// knob keep the bare converter product.
func applyDispatcherShim(sdkReg *sdktools.Registry, cliReg *tools.Registry, opts Options, turn *sdkTurnState) {
	if sdkReg == nil || opts.Dispatcher == nil {
		return
	}
	for _, t := range sdkReg.Tools() {
		st, ok := t.(sdktools.SchemaTool)
		if !ok {
			continue
		}
		name := t.Name()
		var cliTool tools.Tool
		if cliReg != nil {
			if ct, ok := cliReg.Get(name); ok {
				cliTool = ct
			}
		}
		sdkReg.Remove(name)
		wrapped := &dispatcherShim{inner: t, schema: st, cli: cliTool, opts: opts, turn: turn}
		if err := sdkReg.Add(wrapped); err != nil {
			_ = sdkReg.Add(t)
		}
	}
}
