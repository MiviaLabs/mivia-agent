// Package agent - SDK agent-loop adapter.
//
// This file builds the SDK's agentloop.Options from a Loop and CLI
// Options: it converts the CLI registry, wraps the CLI completer, and
// bridges surface rotation. agentloop_run.go drives the built Options
// through the SDK's Loop for one turn (RunAgentLoopOnce) and applies
// the post-run CLI contract.
//
// CLI Options fields split into three groups on the SDK path: carried
// today, accepted semantic gaps, and fail-closed. See
// docs/development/sdk-backend-field-mapping.md for the full table;
// the fail-closed set shrinks as each slice lands.

// The SDK imports are out-of-prefix; the gate filters them out of
// the in-prefix edge set (scripts/check_import_layers.py
// compute_edges), and the policy baseline is unchanged.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// unsupportedSDKOption reports one CLI Options field the SDK path
// cannot carry. The message names the field so the operator can find
// the knob to unset.
func unsupportedSDKOption(field string) error {
	return fmt.Errorf("agent: SDK backend does not support Options.%s", field)
}

// buildAgentLoopOptions projects a Loop and CLI Options onto the
// SDK's agentloop.Options. Completer and Tools come from the Loop;
// the remaining mapped fields come from Options. Every unsupported
// non-zero field fails closed before any conversion runs, so a
// rejected configuration never constructs a half-converted registry.
// It also returns the run's sdkTurnState: the single construction
// site, seeded with the run's surface values, that RunAgentLoopOnce
// consults for bridge errors after the run.
//
// turnUserText is the current turn's user message content, the
// content-match boundary the ContinueOnStop hook's decision helpers
// use to locate the turn inside a StopDecision's History.
func buildAgentLoopOptions(l *Loop, opts Options, turnUserText string) (sdkagentloop.Options, *sdkTurnState, error) {
	if err := rejectUnsupportedSDKBatches(opts); err != nil {
		return sdkagentloop.Options{}, nil, err
	}
	turn := newSDKTurnState()
	seedSDKTurnState(turn, opts)
	// Item 8: WorkLimits token reservations ride the SDK's WorkBudget
	// hook over the loop's workLimitMeter, with the legacy outputCap
	// clamp on Options.MaxTokens.
	budgetHook, clampedMaxTokens, err := newSDKWorkBudgetHook(l, opts)
	if err != nil {
		return sdkagentloop.Options{}, nil, err
	}
	completer, err := newSDKTurnCompleter(l, opts, turn, clampedMaxTokens)
	if err != nil {
		return sdkagentloop.Options{}, nil, err
	}
	sdkTools, err := buildSDKToolRegistry(l, opts, l.Tools, turn)
	if err != nil {
		return sdkagentloop.Options{}, nil, err
	}
	// MaxSteps passes through verbatim. The SDK's Validate accepts 0
	// and treats it as uncapped via unboundedOrSet (MaxInt32), matching
	// the legacy loop's MaxSteps <= 0 == unbounded contract. A
	// positive MaxSteps is the requested cap as-is. The previous
	// defaultSDKMaxIterations = 25 substitution was removed: it capped
	// the SDK path at a value the legacy loop never honored, breaking
	// the parity the field-mapping doc advertises.
	maxIterations := opts.MaxSteps
	out := sdkagentloop.Options{
		Completer:          completer,
		Tools:              sdkTools,
		Model:              opts.Model,
		MaxIterations:      maxIterations,
		MaxCallsPerTurn:    opts.MaxToolCallsPerBatch,
		MaxConcurrentTools: opts.MaxConcurrentTools,
		SessionID:          opts.SessionID,
	}
	attachSDKObservability(&out, opts, turn)
	// BatchResultBudgetBytes > 0 is carried by the host-side turn
	// shaping wrapper applied above (applyTurnShaping); the SDK's
	// TurnResultBudget stays unset because its semantics (omit the
	// over-budget result) contradict the CLI's degrade-with-notice
	// contract. The negative derived-budget mode has no SDK analogue
	// and was rejected above.
	// WorkLimits.MaxTurns clamps MaxIterations, mirroring the legacy
	// clamp at loop.go's runOnceLegacy (see applySDKStepBound).
	applySDKStepBound(&out, opts)
	// Stop-time continuations ride the SDK's own ContinueOnStop hook;
	// see installSDKContinueOnStop.
	installSDKContinueOnStop(l, &out, opts, turn, turnUserText)
	// MaxToolCalls rides the ToolBudget bridge (agentloop_toolbudget.go),
	// sharing l.workLimits with the WorkBudget bridge above.
	out.WorkBudget = budgetHook
	out.ToolBudget = newSDKToolBudget(l)
	// Surface rotation: the CLI's per-step Surface hook (legacy
	// applySurfaceHook) bridges onto the SDK's own Options.Surface,
	// consulted at the top of every iteration from the second one on -
	// the same skip-step-1 rule the legacy loop applies. The bridge
	// records the rotation's Dispatcher/RemainderSpool into the turn
	// state (so the per-call shims read the live values), rebuilds the
	// converted registry from the rotation's CLI registry through the
	// same shim chain (sharing the turn's shaping counter), and
	// re-advertises the rotation's pinned ToolSpecs. A registry
	// conversion failure is recorded in the turn state and the hook
	// returns nil (keep prior surface); RunAgentLoopOnce fails the run
	// with the recorded error after RunSteerable returns.
	if opts.Surface != nil {
		out.Surface = bridgeSDKBridgeSurface(l, opts, turn)
	}
	// WatchdogInterval deliberately does NOT map to
	// HeartbeatInterval: a positive HeartbeatInterval requires a Bus
	// the CLI path does not wire, and Validate would reject the
	// options. The watchdog's steer-latency role is carried by the
	// MailboxPending poller in the steer bridge instead.
	return out, turn, nil
}

// applySDKStepBound sets the SDK iteration bound for the turn:
// MaxSteps passes through verbatim. The SDK's Validate accepts 0
// and treats it as uncapped via unboundedOrSet (MaxInt32), matching
// the legacy loop's MaxSteps <= 0 == unbounded contract. A
// positive MaxSteps is the requested cap as-is. The previous
// defaultSDKMaxIterations = 25 substitution was removed: it capped
// the SDK path at a value the legacy loop never honored, breaking
// the parity the field-mapping doc advertises.
//
// WorkLimits.MaxTurns clamps MaxIterations, mirroring the legacy
// clamp at loop.go's runOnceLegacy: the test reads opts.MaxSteps
// (pre-default) because an unset MaxSteps means unbounded, so ANY
// positive turn limit becomes the bound even above the default 25.
func applySDKStepBound(out *sdkagentloop.Options, opts Options) {
	if limit := opts.WorkLimits.MaxTurns; limit > 0 && (opts.MaxSteps <= 0 || limit < opts.MaxSteps) {
		out.MaxIterations = limit
	}
}

// installSDKContinueOnStop wires the turn's stop-time continuation
// callback. The bounded empty-response retry and the bounded unacted
// continuation ride the SDK's own ContinueOnStop hook (since
// mivia-ai-sdk v0.1.3): they are iterations of THIS loop now, so its
// MaxIterations is the per-turn total by itself. Installed after the
// clamp in applySDKStepBound so the hook's budget guard reads the
// effective bound, and sharing the turn state so the guard counts
// every completed call of the turn (the deleted replay_step_budget.go's
// rule, which outlived that file's budget arithmetic).
func installSDKContinueOnStop(l *Loop, out *sdkagentloop.Options, opts Options, turn *sdkTurnState, turnUserText string) {
	out.ContinueOnStop = newSDKContinueOnStop(l, *out, opts, turn, turnUserText)
}

// attachSDKObservability wires the run's live stream tee. The operator wire
// dump is NOT wired here: it needs the effective request, which only exists
// after the completer merges the turn defaults, so it hangs off the
// completer's own per-call seam instead (newSDKTurnCompleter).
func attachSDKObservability(out *sdkagentloop.Options, opts Options, turn *sdkTurnState) {
	attachSDKStreamingWriter(out, opts, turn)
}

// attachSDKStreamingWriter tees FinalWriter to the SDK's StreamingWriter
// and saves the tee on sdkTurnState for interrupted cancel recovery.
func attachSDKStreamingWriter(out *sdkagentloop.Options, opts Options, turn *sdkTurnState) {
	if opts.FinalWriter != nil {
		tw := &teeWriter{w: opts.FinalWriter, opts: opts}
		out.StreamingWriter = tw
		turn.setStreamTee(tw)
	}
}

// newSDKTurnCompleter wraps the CLI completer with the run's turn
// defaults and the turn-state step counter (extracted from
// buildAgentLoopOptions to keep it under the function-size budget).
// The usage callback carries the legacy calibration: every completed
// Chat call reports its CLI request/response pair through
// l.emitTurnUsage, the same update the legacy loop runs per completion
// (loop.go's emitTurnUsage call site). ChatStream stays unwired -
// it yields no usage response, and the agent loop calls Chat anyway.
func newSDKTurnCompleter(l *Loop, opts Options, turn *sdkTurnState, clampedMaxTokens *int) (*agentLoopCompleter, error) {
	// The operator wire dump rides this same seam: it is the only place
	// that sees the EFFECTIVE request (post mergeTurnDefaults), which is
	// what an operator needs to read. Nil unless the operator named a
	// directory, so the default build pays nothing (audit_dump.go).
	dump := newProviderAuditDump(opts.SessionID)
	onUsage := func(ctx context.Context, cliReq provider.Request, resp *provider.Response) {
		emitReasoning(opts, resp)
		if dump != nil {
			dump(cliReq, resp, turn.currentStep())
		}
		estimatedTokens, _ := provider.EstimatePromptCost(cliReq.Messages, cliReq.Tools, l.contextAccounting())
		l.emitTurnUsage(ctx, opts, cliReq, resp, estimatedTokens)
	}
	completer, err := newAgentLoopCompleterWithDefaults(l.Completer, turnRequestDefaults{
		reasoning:             opts.Reasoning,
		maxTokens:             clampedMaxTokens,
		temperature:           opts.Temperature,
		requestTimeout:        opts.RequestTimeout,
		disableProviderReplay: opts.DisableProviderReplay,
		sessionID:             opts.SessionID,
		streamTransport:       opts.WireStreamTransport,
	}, func(finishReason string) { l.LastFinishReason = finishReason }, func() { turn.steps.Add(1) }, onUsage)
	if err != nil {
		return nil, err
	}
	completer.advertised = turn.currentAdvertised
	return completer, nil
}

// buildSDKToolRegistry converts one CLI registry onto a fully
// shimmed SDK registry: admission predicates, the dispatcher shim
// (innermost), the ref-only shim, and the turn shaping wrapper
// (outermost). It is the ONE construction path for SDK registries:
// the turn-start build and every mid-run surface rotation rebuild go
// through it, so a rotated registry carries the identical execution
// contract and shares the turn state (shaping counter, live
// dispatcher/spool).
func buildSDKToolRegistry(l *Loop, opts Options, cliReg *tools.Registry, turn *sdkTurnState) (*sdktools.Registry, error) {
	// Registry-wide run backstop: a positive Options.ToolRunTimeout is
	// the configured bound for tools with no declared Capability.Timeout;
	// <= 0 maps to TimeoutNone (no cap) so the SDK's hardcoded 10-minute
	// DefaultRunTimeout can never undercut the CLI dispatcher's own
	// per-call deadlines (armDispatcherTimeout).
	runTimeout := sdktools.TimeoutNone
	if opts.ToolRunTimeout > 0 {
		runTimeout = opts.ToolRunTimeout
	}
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg, sdktools.WithDefaultRunTimeout(runTimeout))
	if err != nil {
		return nil, err
	}
	applyDispatcherShim(sdkReg, cliReg, opts, turn)
	emitPending := func(toolCallID, name, detail, input string) {
		// toolCallID is the in-flight call id from the SDK's
		// toolcallctx. It must reach EventToolPending.ToolCallID so the
		// UI's approval resolver can match a user decision back to this
		// specific gate; without it, every Resolve is a silent no-op and
		// the gate blocks forever after approval. The legacy path stamps
		// the same field from task.call.ID at loop_tool_exec.go:70; this
		// closure is its SDK-path equivalent.
		emit(opts, Event{
			Kind:       EventToolPending,
			ToolCallID: toolCallID,
			Name:       name,
			Detail:     detail,
			Input:      input,
		})
	}
	recordDenied := func(toolCallID, name, reason string) {
		// Record an OUTCOME rather than emitting a tool_end directly. The
		// loop's emitter already owns that emission, and emitting here as
		// well would give a refused call two tool_end events. Recording is
		// also what stops the no-outcome fallback from claiming the call
		// "completed (duplicate)" - the reason a denial used to reach every
		// viewer as a success.
		turn.recordToolOutcome(toolCallID, name, "tool call denied by user: "+reason, true)
	}
	if err := sdkadapter.WrapRegistryWithAdmission(sdkReg, cliReg, sdkadapter.AdmissionPredicates{
		ApprovalGate:     opts.ApprovalGate,
		ApprovalStanding: opts.ApprovalStanding,
		ApprovalPolicy:   opts.ApprovalPolicy,
		EmitPending:      emitPending,
		RecordDenied:     recordDenied,
		// StagedMessage and UnadmittedToolHandler are NOT threaded into
		// the SDK admission wrapper on purpose: the SDK's decodeAndRun
		// rejects calls to tools absent from the SDK registry BEFORE
		// any host wrapper runs, surfacing the failure through
		// Options.OnToolCallError (sdkToolCallErrorReporter) where the
		// legacy precedence - StagedMessage first, then
		// UnadmittedToolHandler, then the generic "not available"
		// message - is preserved. Threading them into the admission
		// adapter as well would auto-stage every call to a tool the
		// model has seen on the wire (including load_tools itself),
		// silently bypassing the approval gate the user expected.
	}); err != nil {
		return nil, err
	}
	applyRefOnlyShim(sdkReg, cliReg, opts.RefOnlyTools, turn.currentSpool(), BatchDegradeFloorBytes, opts.SessionID, turn)
	// Host-side turn shaping replaces the SDK's TurnResultBudget: the
	// CLI contract degrades with an honest notice and never omits a
	// call, so the SDK's TurnResultBudget stays unset.
	applyTurnShaping(sdkReg, cliReg, opts, turn)
	return sdkReg, nil
}

// bridgeSDKBridgeSurface adapts the CLI's per-step Surface hook onto
// the SDK's Options.Surface. Non-nil rotation fields install: the
// Dispatcher and RemainderSpool go to the turn state (per-call shim
// reads), the Registry rebuilds through buildSDKToolRegistry, and
// ToolSpecs re-advertise as SDK ToolDefinitions. On a registry
// conversion failure the error is recorded in the turn state and the
// hook returns nil so the SDK keeps the prior surface for the step;
// RunAgentLoopOnce fails the run with the recorded error afterwards.
func bridgeSDKBridgeSurface(l *Loop, opts Options, turn *sdkTurnState) func() *sdkagentloop.Surface {
	return func() *sdkagentloop.Surface {
		surf := opts.Surface()
		turn.rotateSurface(surf.Dispatcher, surf.RemainderSpool)
		// Non-nil ToolSpecs replace the advertised snapshot (the legacy
		// keep-rule: nil keeps the prior one), and the completer override
		// carries them onto the wire from the next request.
		if surf.ToolSpecs != nil {
			turn.setAdvertised(surf.ToolSpecs)
		}
		out := &sdkagentloop.Surface{}
		if surf.ToolSpecs != nil {
			out.Advertised = cliToolSpecsToSDKDefs(surf.ToolSpecs)
		}
		if surf.Registry != nil {
			reg, err := buildSDKToolRegistry(l, opts, surf.Registry, turn)
			if err != nil {
				turn.recordBridgeError(fmt.Errorf("agent: SDK surface rotation: %w", err))
				return nil
			}
			out.Registry = reg
		}
		// A rotation that carries neither ToolSpecs nor a Registry keeps
		// the SDK's prior surface (nil return, the SDK's documented
		// nil-keeps-prior contract). Returning the empty non-nil Surface
		// instead would make the SDK's apply treat it as a wholesale
		// replace: defs and schemas cleared, so every later tool call
		// fails with ErrToolNotOffered.
		if out.Advertised == nil && out.Registry == nil {
			return nil
		}
		return out
	}
}

// cliToolSpecsToSDKDefs converts the CLI's OpenAI-shaped ToolSpec
// maps (the pinned advertised snapshot a Surface hook returns) into
// the SDK's ToolDefinition list. A spec without a function name or
// function block is silently dropped from the model-visible
// advertised set - returning it would either crash the SDK schema
// compiler or leave the model with an un-callable name. The drop
// stays silent because the caller (a Surface hook returning the
// session's admissible union) already vetted every name; an empty
// name on the wire is an upstream bug rather than an end-user
// signal. Missing parameters fall back to an empty object schema so
// the definition still compiles.
func cliToolSpecsToSDKDefs(specs []provider.ToolSpec) []sdkshape.ToolDefinition {
	out := make([]sdkshape.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		description, _ := fn["description"].(string)
		schema, err := json.Marshal(fn["parameters"])
		if err != nil || fn["parameters"] == nil {
			schema = []byte(`{"type":"object"}`)
		}
		out = append(out, sdkshape.ToolDefinition{Name: name, Description: description, Schema: schema})
	}
	return out
}

// rejectUnsupportedSDKBatches fails closed on every CLI Options field
// whose semantics the SDK path cannot carry. Zero values pass: a
// caller that never set the knob loses nothing by switching backends.
//
// Fields the SDK accepts at zero but interprets differently are NOT
// rejected here: the accepted-semantic-gap table lives on the
// agentloop adapter's package doc. Today that is a negative
// BatchResultBudgetBytes (the SDK's TurnResultBudget is a literal
// byte budget only, not the CLI's "derived from MaxContextTokens" mode).
// It passes through to the SDK silently; the CLI caller accepts the
// difference. MaxConcurrentTools is carried via sdkagentloop.Options.MaxConcurrentTools.
func rejectUnsupportedSDKBatches(opts Options) error {
	// All options are carried on the SDK path: Surface via bridgeSDKBridgeSurface,
	// BeforeStep via Steer injector, RefOnlyTools and RemainderSpool via ref-only shim,
	// BatchResultBudgetBytes via TurnResultBudget, MailboxPendingInterrupt via bridgeSteerSignals,
	// WorkLimits fields via buildAgentLoopOptions/WorkBudget/ToolBudget bridges,
	// and MaxContextTokens, OnEvent, EventBus, UsageWriter, FinalWriter,
	// RequireFinalText, SummaryConfig.Summarizer, StagedToolMessage,
	// UnadmittedToolHandler, ApprovalGate, ApprovalStanding, Dispatcher,
	// MaxToolResultChars, ToolTimeout, ToolRunTimeout, Reasoning, MaxTokens, Temperature,
	// RequestTimeout, and DisableProviderReplay.
	// See docs/development/sdk-backend-field-mapping.md §1 for the full carrier table.
	return nil
}
