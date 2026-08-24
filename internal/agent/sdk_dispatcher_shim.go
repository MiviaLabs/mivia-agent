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
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
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
	pass1      pass1Holder
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
	// Tool-event synthesis state (sdk_tool_events.go): the call
	// PointPreTool stashed (pendingTool), the executed call's recorded
	// outcome (toolOutcome), and the once-per-iteration stream-revoke
	// gate (streamRevoked, armed at the first tool call, reset by the
	// EventIterationStart bus subscription).
	toolMu        sync.Mutex
	pendingTool   *sdkshape.ToolCall
	toolOutcome   *toolCallOutcome
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
	s.shapeOnce.Do(func() { s.shape = &turnShapeCounter{} })
	return s.shape
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

// pass1Holder hands one pass-1 resultParts from the dispatcher shim
// (innermost) to the turn shaping wrapper (outermost). The SDK runs
// tool calls sequentially, so the newest record always belongs to the
// call whose wrapper chain is currently unwinding.
type pass1Holder struct {
	mu    sync.Mutex
	parts resultParts
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
// The clock starts here because the SDK runs calls one at a time. A
// zero resolution leaves ctx untouched.
func armDispatcherTimeout(ctx context.Context, opts Options, args []byte, capability tools.Capability) (context.Context, context.CancelFunc, time.Duration) {
	callTimeout := resolveToolCallTimeout(opts.ToolTimeout, capability.Timeout)
	if requested := requestedToolTimeout(args); requested > callTimeout {
		callTimeout = clampToDeadline(ctx, requested)
	}
	if callTimeout <= 0 {
		return ctx, func() {}, 0
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
	// RunAgentLoopOnce guarantees a non-nil opts.Dispatcher before tools
	// are wrapped (either the caller wired one or a scoped dispatcher
	// was built from l.Tools). A nil dispatcher at this point is a
	// programmer error in package wiring, not a runtime condition;
	// dispatch via the caller's hook has nothing to attach to, so the
	// dispatcher must be there. A surface rotation may have swapped
	// the dispatcher or spool since the wrap; the turn state carries
	// the live values and wins over the wrap-time Options copy.
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
	// Legacy "running" tool_start: the analogue of loop_tool_exec.go's
	// pre-dispatch emission, keyed on the call PointPreTool stashed.
	if d.turn != nil {
		if pending := d.turn.currentPendingToolCall(); pending != nil {
			emit(d.opts, Event{Kind: EventToolStart, ToolCallID: pending.ID, Name: d.inner.Name(), Detail: "running"})
		}
	}
	r := dispatcher.Invoke(ctx, runtime.Request{
		ParentID: d.opts.ParentID, TurnID: d.opts.TurnID, SessionID: d.opts.SessionID,
		Role: d.opts.Role, Depth: d.opts.Depth, Budget: d.opts.Budget,
		Kind: runtime.Tool, Name: d.inner.Name(), Input: args, Timeout: callTimeout,
		Step: d.turn.currentStep(), SkipDedup: !capability.Dedups(),
	})
	result = string(r.Output)
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
	if d.turn != nil {
		d.turn.pass1.store(resultParts{
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
	d.recordToolEventOutcome(args, body, r.Err, ephemeral)
	return sdktools.Out{Value: body}, nil
}

// recordToolEventOutcome records the outcome the EventToolCallEnd
// handler renders the legacy tool_end from (sdk_tool_events.go): the
// final model-visible body and the dispatcher's failure flag. An
// ephemeral tool's marker overrides only the operator preview
// (loop_tools.go emitToolEnd's rule); a later shim (ref-only notice,
// turn-shaping re-cut) that rewrites the body overwrites the record so
// tool_end matches the post-shaping body.
func (d *dispatcherShim) recordToolEventOutcome(args []byte, body string, dispatchErr error, ephemeral bool) {
	if d.turn == nil {
		return
	}
	preview := ""
	if ephemeral {
		if et, ok := d.cli.(tools.EphemeralResultTool); ok {
			preview = et.EphemeralResultMarker(args)
		}
	}
	d.turn.recordToolOutcomeWithPreview(callIDOf(d.turn), d.inner.Name(), body, dispatchErr != nil, preview)
}

// callIDOf returns the pending call's ID, or "" when none is stashed.
func callIDOf(turn *sdkTurnState) string {
	if pending := turn.currentPendingToolCall(); pending != nil {
		return pending.ID
	}
	return ""
}

// store records the newest pass-1 parts.
func (h *pass1Holder) store(p resultParts) {
	h.mu.Lock()
	h.parts = p
	h.mu.Unlock()
}

// take returns the stored parts when body is exactly the parts'
// model-visible output (hook context appended), clearing the record.
// A body rewritten by an intermediate shim (the ref-only notice)
// misses and leaves the caller on its default single-pass path.
func (h *pass1Holder) take(body string) (resultParts, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.parts
	if p.cappedBody == "" {
		return resultParts{}, false
	}
	if body != appendHookContext(p.cappedBody, p.hookContext) {
		return resultParts{}, false
	}
	h.parts = resultParts{}
	return p, true
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
