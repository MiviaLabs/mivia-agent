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
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

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
var _ sdktools.ProfiledTool = (*dispatcherShim)(nil)

func (d *dispatcherShim) Name() string { return d.inner.Name() }

// ExecutionProfile forwards the inner adapter's profile (the
// sdkToolAdapter's CLI Capability bridge). The SDK's run-timeout
// resolver consults only the OUTERMOST registered value and Go
// interface wrappers silently strip optional interfaces, so every
// shim layer forwards explicitly; without this the SDK backstop
// would cap a 12h-budget tool at its own registry default
// (dispatch_tasks killed at the SDK's hardcoded 10 minutes).
//
// A declared positive Capability.Timeout is published as TimeoutNone,
// not verbatim: THIS shim already arms the declared budget (plus any
// model-requested timeout_seconds raise) as a real context deadline
// in armDispatcherTimeout and renders expiry as the tool's graceful
// bounded envelope (run_command's "exit=timeout" + truncated output).
// A verbatim SDK bound would expire at the same instant and win the
// race, replacing that envelope with a bare ErrRunTimeout - and a
// static profile can never see a per-call raise, so it would also
// kill budgets the shim legitimately extended. An undeclared (zero)
// Timeout passes through, so the [tools] tool_run_timeout_seconds
// registry default still backstops profile-less tools.
func (d *dispatcherShim) ExecutionProfile() sdktools.ExecutionProfile {
	p := sdktools.ExecutionProfileOf(d.inner)
	if p.Timeout > 0 {
		p.Timeout = sdktools.TimeoutNone
	}
	return p
}

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
	callTimeout := ResolveToolCallTimeout(ctx, opts.ToolTimeout, args, capability)
	narrowed, cancel := context.WithTimeout(ctx, callTimeout)
	return narrowed, cancel, callTimeout
}

// ResolveToolCallTimeout is the budget one tool call gets: the tool's declared
// Capability.Timeout, else the session default, else DefaultToolTimeout - and
// a larger model-requested timeout_seconds when the parent deadline leaves
// room for it.
//
// Exported as a DURATION rather than as a narrowed context because the
// deferred-tool path (internal/chat) needs the same number and must NOT
// narrow its own ctx. Its approval decision is inline and happens before the
// dispatcher call, so a narrowed ctx there would put this deadline around the
// operator reading the prompt: uiadapter's gate selects on ctx.Done() and
// answers "canceled", which would auto-deny a prompt mid-read and report a
// refusal nobody made. That path passes the duration as Request.Timeout
// instead, and the dispatcher arms it around the handler alone - which is
// also all it covers here, since the approval wrapper sits OUTSIDE this shim.
func ResolveToolCallTimeout(ctx context.Context, toolTimeout time.Duration, args []byte, capability tools.Capability) time.Duration {
	callTimeout := resolveToolCallTimeout(toolTimeout, capability.Timeout)
	if requested := requestedToolTimeout(args); requested > callTimeout {
		if clamped := clampToDeadline(ctx, requested); clamped > 0 {
			callTimeout = clamped
		}
	}
	return callTimeout
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
	// tools.CapabilityOf, not a zero Capability. The zero value's Class is
	// ExecutionRead (iota 0), so Dedups() was false and SkipDedup was TRUE for
	// any tool that declares no capability - meaning an unclassified tool
	// never deduped and a duplicate delivery re-ran its side effect. The
	// canonical default is ExecutionExternal: a tool that says nothing about
	// itself is assumed to have side effects.
	//
	// Unclassified is not hypothetical - workflow_run, workflow_deliver,
	// post_message and every ledger tool ship without a Capability method -
	// and d.cli is itself nil whenever an SDK tool has no CLI registry match,
	// which CapabilityOf also answers with the safe default.
	capability := tools.CapabilityOf(d.cli, args)
	ctx, cancelTimeout, callTimeout := armDispatcherTimeout(ctx, d.opts, args, capability)
	defer cancelTimeout()
	dispatcher, spool := d.dispatcherAndSpool()
	callKey := toolCallKeyFromContext(ctx, d.inner.Name())
	ctx, cleanupExplicitCancel := d.armExplicitCancel(ctx, callKey)
	defer cleanupExplicitCancel()
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
	return d.composeRunOutput(callKey, args, r, spool, capability)
}

// armExplicitCancel derives a second, independent cancel from ctx (already
// narrowed by armDispatcherTimeout) and registers it in d.turn's registry
// under callKey, so an external cancel-by-ID request can end this call
// without waiting for its timeout. Both RunUnadmittedTool's throwaway shim
// and the registry-installed shim funnel through Run, so this is the single
// site both paths register through. The returned cleanup func cancels the
// context and deregisters the call; callers defer it once.
func (d *dispatcherShim) armExplicitCancel(ctx context.Context, callKey string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	if d.turn == nil || callKey == "" {
		return ctx, cancel
	}
	d.turn.registerCancel(callKey, cancel)
	return ctx, func() {
		d.turn.deregisterCancel(callKey)
		cancel()
	}
}

// composeRunOutput turns one dispatcher outcome into Run's model-visible
// result: capped, spooled, hook-annotated content and a recorded outcome -
// split out of Run to keep it under the per-function line budget. Always
// returns a nil error: see Run's own doc comment for why an errored call
// must never surface as a hard Run failure.
func (d *dispatcherShim) composeRunOutput(callKey string, args []byte, r runtime.Result, spool *remainder.Spool, capability tools.Capability) (sdktools.Out, error) {
	// A dedup-served duplicate is answered with the OWNER's HookRuns (DC-9
	// fidelity, runtime/dispatcher.go), which did not run for THIS call - so
	// emitting them here would show a hook firing that never happened for
	// this invocation. Only the owner's own Run reaches this branch with
	// IsDuplicate() false.
	if callKey != "" && !r.IsDuplicate() {
		emitHookRuns(d.opts, callKey, r.HookRuns)
	}
	// A duplicate never re-ran: the model's result is the suppression
	// notice, but the OWNER's pre-rewrite body is what failed-duplicate
	// detection must scan (a run_command duplicate reports its non-zero
	// child exit in the recorded header with err=nil; toolResultBodyFailed
	// in loop_tools.go operates on the originalBody for exactly this
	// reason). Capture it BEFORE the notice rewrite.
	originalBody := string(r.Output)
	result := originalBody
	if r.IsDuplicate() {
		result = duplicateDeliveryNotice
	} else if r.Err != nil && strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("error: %v", r.Err)
	}
	hookContext := r.HookContext
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
	if d.turn != nil {
		failed := r.Err != nil || toolResultBodyFailed(d.inner.Name(), originalBody)
		if reminder := d.turn.recordFailure(failed); reminder != "" {
			body = AppendSystemReminder(body, reminder)
		}
	}
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

// applyDispatcherShim wraps every tool in the converted SDK registry
// with the dispatcher shim. It is a no-op when no dispatcher is wired
// and no result cap is configured, so callers that never set either
// knob keep the bare converter product.
// applyDispatcherShim wraps every tool in sdkReg so it executes through the
// dispatcher, and REFUSES rather than leaving one ungoverned.
//
// The shim is where the per-call timeout, the dedup declaration, the result
// cap, the hook gate and advisory, the duplicate rules and the failure
// outcome live. A tool the model can call without it runs with none of them.
//
// This used to degrade silently in three ways instead of refusing: a nil
// dispatcher returned early and left the WHOLE registry unwrapped, a
// non-SchemaTool was skipped, and a failed re-Add put the ORIGINAL ungoverned
// tool back. None was reachable in a shipped session - composition always
// builds a dispatcher and every adapter is a SchemaTool - but that was a
// property of the callers, not of this code, and the failure mode was every
// contract dropped at once with nothing logged.
//
// An empty registry with no dispatcher is fine: there is nothing to govern.
func applyDispatcherShim(sdkReg *sdktools.Registry, cliReg *tools.Registry, opts Options, turn *sdkTurnState) error {
	if sdkReg == nil || len(sdkReg.Tools()) == 0 {
		return nil
	}
	// Both the wrap-time value AND the turn's live one: Run executes against
	// turn.currentDispatcher(), which a surface rotation can populate after
	// this check. Testing only opts.Dispatcher would refuse a turn that is in
	// fact governed; testing only the live one would miss a turn that never
	// rotates. Refuse when NEITHER can govern the call.
	if opts.Dispatcher == nil && (turn == nil || turn.currentDispatcher() == nil) {
		return fmt.Errorf("agent: no dispatcher wired, so %d tool(s) would execute "+
			"with no timeout, dedup, result cap, hooks or recorded outcome",
			len(sdkReg.Tools()))
	}
	for _, t := range sdkReg.Tools() {
		st, ok := t.(sdktools.SchemaTool)
		if !ok {
			return fmt.Errorf("agent: tool %q carries no schema, so it cannot be "+
				"governed by the dispatcher", t.Name())
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
			// Deliberately NOT re-adding t. Restoring the unwrapped tool is
			// how the failure path handed the model an ungoverned one.
			return fmt.Errorf("agent: tool %q: install dispatcher shim: %w", name, err)
		}
	}
	return nil
}

// RunUnadmittedTool executes ONE tool the host authorized for this call but
// that is absent from the SDK registry, through the SAME shim an admitted
// call uses.
//
// This is the fix for DC-35. The deferred path used to invoke the runtime
// dispatcher itself, which made it a second implementation of tool execution:
// the timeout, the dedup declaration, the result cap, the hook plumbing, the
// duplicate contracts and the failure outcome all had to be re-honoured by
// hand, and five of them were not. Each omission shipped as its own bug.
// Delegating here means those contracts hold by construction rather than by
// anyone remembering them, and a tenth contract added to Run tomorrow reaches
// this path for free.
//
// It does NOT decide approval. That decision belongs to the host, which must
// make it before it charges an admission attempt or stages a publication for
// the call - so it happens upstream, and this function only executes what the
// host already approved.
//
// opts and turn are the loop's own, so the call lands in the same turn state,
// dedup buckets and outcome record as every other call in the turn.
func RunUnadmittedTool(ctx context.Context, opts Options, turn *sdkTurnState, cliTool tools.Tool, args json.RawMessage) (string, error) {
	inner, err := sdkadapter.ConvertTool(cliTool)
	if err != nil {
		return "", err
	}
	// Through DecodeArguments, exactly as an admitted call is. This path used
	// to hand the raw bytes straight to Run, so it skipped the validity check
	// the adapter performs - and json.Marshal of a RawMessage holding only
	// whitespace fails deep inside Run, where the error had nowhere sensible
	// to go. Validating here rejects it with the tool's name instead.
	in, err := inner.DecodeArguments(args)
	if err != nil {
		return "", err
	}
	shim := &dispatcherShim{inner: inner, schema: inner, cli: cliTool, opts: opts, turn: turn}
	// Ref-only spooling is applied by wrapping registry tools, and this tool is
	// deliberately not in the registry - so it has to be wrapped here, or an
	// operator's ref_only_tools entry would apply to a deferred tool only
	// after it loaded.
	// Same wrapper stack the registry builds, in the same order: the
	// dispatcher shim innermost, then ref-only, then turn shaping. Both outer
	// layers wrap REGISTRY tools, and this tool is deliberately not in the
	// registry, so each has to be applied here or the deferred call escapes
	// it - ref_only_tools silently inlining a body, and a deferred result
	// never charged against the turn's batch budget.
	runner := wrapTurnShaping(wrapRefOnly(shim, cliTool, opts, turn), cliTool, opts, turn)
	out, err := runner.Run(ctx, in)
	if err != nil {
		return "", err
	}
	body, _ := out.Value.(string)
	// Drain the pass-1 entry the shim stored. The shaping wrapper consumes it
	// when a batch budget is active; with shaping off nothing does, and the
	// entry holds this call's capped body for the life of the turn state -
	// which pass1Map's own comment names as a leak. take deletes either way,
	// so this is a no-op when shaping already claimed it.
	if turn != nil {
		if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
			turn.pass1.take(tc.ID, body)
		}
	}
	return body, nil
}
