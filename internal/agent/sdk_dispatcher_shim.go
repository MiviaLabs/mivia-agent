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

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// sdkTurnState is the per-run state shared by the SDK path's
// completer wrapper and tool shims. steps counts completed Completer
// calls (the SDK loop's iteration counter as the host observes it) so
// tool dispatches can stamp runtime.Request.Step and re-issue an
// identical read in a later iteration without the turn-scoped dedup
// suppressing it. pass1 carries the newest pass-1 result parts from
// the dispatcher shim to the turn shaping wrapper so a budget degrade
// reports the ORIGINAL body's true total and pages the original
// bytes, exactly like the legacy shapeBatch chain.
type sdkTurnState struct {
	steps atomic.Int64
	pass1 pass1Holder
}

func newSDKTurnState() *sdkTurnState { return &sdkTurnState{} }

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
	// Per-call timeout resolution mirrors prepareToolTasks: the tool's
	// own requested timeout (when larger) wins, clamped to the run
	// deadline; the clock starts here because the SDK runs calls one
	// at a time.
	callTimeout := d.opts.ToolTimeout
	if requested := requestedToolTimeout(args); requested > callTimeout {
		callTimeout = clampToDeadline(ctx, requested)
	}
	if callTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	var result, hookContext string
	// RunAgentLoopOnce guarantees a non-nil opts.Dispatcher before tools
	// are wrapped (either the caller wired one or a scoped dispatcher
	// was built from l.Tools). A nil dispatcher at this point is a
	// programmer error in package wiring, not a runtime condition;
	// dispatch via the caller's hook has nothing to attach to, so the
	// dispatcher must be there.
	r := d.opts.Dispatcher.Invoke(ctx, runtime.Request{
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
	spool := d.opts.RemainderSpool
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
	return sdktools.Out{Value: appendHookContext(capped, hookContext)}, nil
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
