// Package agent - SDK agent-loop adapter.
//
// RunAgentLoopOnce drives the SDK's mivia-ai-sdk/agentloop.Loop for
// one turn: it converts the CLI registry, wraps the CLI completer,
// bridges the steer signals, and returns the SDK Result. It is
// ADDITIVE: the legacy (*Loop).Run in loop.go is unchanged, and the
// dispatcher's "sdk" branch (loop_dispatch.go) chooses the runtime.
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
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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
func buildAgentLoopOptions(l *Loop, opts Options) (sdkagentloop.Options, *sdkTurnState, error) {
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
	attachSDKStreamingWriter(&out, opts, turn)
	// BatchResultBudgetBytes > 0 is carried by the host-side turn
	// shaping wrapper applied above (applyTurnShaping); the SDK's
	// TurnResultBudget stays unset because its semantics (omit the
	// over-budget result) contradict the CLI's degrade-with-notice
	// contract. The negative derived-budget mode has no SDK analogue
	// and was rejected above.
	// WorkLimits.MaxTurns clamps MaxIterations, mirroring the legacy
	// clamp at loop.go's runOnceLegacy: the test reads opts.MaxSteps
	// (pre-default) because an unset MaxSteps means unbounded, so ANY
	// positive turn limit becomes the bound even above the default 25.
	if limit := opts.WorkLimits.MaxTurns; limit > 0 && (opts.MaxSteps <= 0 || limit < opts.MaxSteps) {
		out.MaxIterations = limit
	}
	// MaxToolCalls stays fail-closed: the SDK path runs tool calls
	// through the converted registry (see rejectUnsupportedSDKBatches).
	out.WorkBudget = budgetHook
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
	onUsage := func(ctx context.Context, cliReq provider.Request, resp *provider.Response) {
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
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg)
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
	if err := sdkadapter.WrapRegistryWithAdmission(sdkReg, cliReg, sdkadapter.AdmissionPredicates{
		ApprovalGate:     opts.ApprovalGate,
		ApprovalStanding: opts.ApprovalStanding,
		EmitPending:      emitPending,
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
	// Surface is carried via bridgeSDKBridgeSurface (field-mapping doc §1).
	// BeforeStep is carried via the Steer injector installed in
	// RunAgentLoopOnce: RunAgentLoopOnce calls opts.BeforeStep on
	// the loop goroutine at the top of each iteration (after this
	// reject ran) and at every steered-stop downgrade point, then
	// converts the returned CLI messages to SDK shape and appends
	// them to history via agentloop/steer.go's SetInjector; the
	// carrier cell in sdk-backend-field-mapping.md has the details.
	// RefOnlyTools and RemainderSpool are carried by the per-call
	// ref-only shim in the tool-registry converter. The shim mirrors
	// the legacy CLI's refOnlyTier (internal/agent/shape_batch_refonly.go:25-45)
	// faithfully: floor check, tool membership, spool, ref-notice text.
	// Negative BatchResultBudgetBytes derives a budget from
	// MaxContextTokens in the CLI's shape_batch path; the SDK's
	// TurnResultBudget is a literal byte budget only, so this
	// "derived mode" is an accepted semantic gap. A Backend:"sdk"
	// caller with a negative value sees no batch shaping from this
	// knob, not the derived one. The positive form still maps in
	// buildAgentLoopOptions.
	// MailboxPendingInterrupt is wired in bridgeSteerSignals as a
	// fast poll (it gates the interrupt branch; the watchdog branch
	// keeps using MailboxPending).
	// WorkLimits splits: MaxTurns and DeadlineAt are carried (the
	// turn clamp in buildAgentLoopOptions and the deadline narrowing
	// in RunAgentLoopOnce); MaxPromptTokens, MaxOutputTokens, and
	// MaxOutputPerCall ride the WorkBudget bridge (Item 8); and
	// MaxToolCalls still fails closed - the SDK path runs tool calls
	// through the converted registry, so the legacy reserveToolBatch
	// has no call point here.
	if opts.WorkLimits.MaxToolCalls > 0 {
		return unsupportedSDKOption("WorkLimits.MaxToolCalls")
	}
	// PreserveWorkLimits passes: the flag preserves the cumulative
	// token-reservation counters, and those now ride the WorkBudget
	// bridge (newSDKWorkBudget applies the same reset rule the legacy
	// runOnceLegacy does), so the flag means the same thing on both
	// backends.
	// MaxContextTokens, OnEvent, EventBus, UsageWriter, FinalWriter,
	// RequireFinalText, SummaryConfig.Summarizer, StagedToolMessage,
	// UnadmittedToolHandler, ApprovalGate, ApprovalStanding, RefOnlyTools,
	// RemainderSpool, Dispatcher, MaxToolResultChars, ToolTimeout, Reasoning,
	// MaxTokens, Temperature, RequestTimeout, DisableProviderReplay, and
	// MailboxPendingInterrupt are carried. The full carrier table -
	// placement, ordering, and each field's mechanism - lives in
	// docs/development/sdk-backend-field-mapping.md §1.
	return nil
}

// RunAgentLoopOnce drives one SDK-backed agent-loop turn for the
// completer and registry carried by l, with CLI-shape opts and
// messages. It fail-closes on unsupported Options fields, converts
// the registry, bridges InterruptCh and MailboxPending onto a Steer,
// and returns the SDK Result of RunSteerable.
//
// The steer bridge spawns at most two goroutines, both of which exit
// on ctx.Done: one resolves InterruptCh once and fires Trigger when
// the channel closes; one polls MailboxPending on a ticker (the
// WatchdogInterval when positive, else 250ms) and fires Trigger when
// the predicate returns true. A nil InterruptCh or MailboxPending
// spawns nothing.
func RunAgentLoopOnce(ctx context.Context, l *Loop, opts Options, msgs []provider.Message) (sdkagentloop.Result, error) {
	// WorkLimits.DeadlineAt narrows the context deadline, mirroring
	// the legacy narrowing at loop.go's runOnceLegacy: the earlier of
	// the parent deadline and the work deadline wins; an unset parent
	// takes the work deadline as-is.
	if deadline := opts.WorkLimits.DeadlineAt; !deadline.IsZero() {
		if parent, ok := ctx.Deadline(); !ok || deadline.Before(parent) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
	}
	// The initial history reaches the SDK unprepared: host-side
	// preparation runs per iteration through sdkOpts.Trim below (the
	// SDK applies Trim before every Completer call, iteration 1
	// included), which is also what honors MaxContextTokens; the
	// SDK's Window stays nil (see sdk_prepare.go).
	preparedMsgs, err := sdkInitialHistory(ctx, l, opts, msgs)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	if err := sdkPromptBudgetPreflight(l, opts, msgs); err != nil {
		return sdkagentloop.Result{}, err
	}
	// Tool execution on the SDK path routes through a runtime
	// dispatcher, like the legacy executeToolTask. A caller that
	// wired none gets a scoped one over the loop's registry (the
	// same construction the subagent handler uses), closed with the
	// run.
	if closer, err := ensureSDKDispatcher(l, &opts); err != nil {
		return sdkagentloop.Result{}, err
	} else {
		defer closer()
	}
	sdkOpts, turn, err := buildAgentLoopOptions(l, opts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	sdkOpts.Trim = sdkPrepareTrim(l, opts)
	// Legacy not-in-registry denial (agentloop_tool_error.go; the
	// reporter records a failed outcome for its synthesized denials)
	// and the legacy tool_start/tool_end wire shape (sdk_tool_events.go).
	sdkOpts.OnToolCallError = sdkToolCallErrorReporter(opts, turn)
	sdkOpts.Hooks = sdkToolEventHooks(opts, turn)
	if opts.OnEvent != nil || opts.EventBus != nil || opts.FinalWriter != nil {
		// The SDK (since mivia-ai-sdk commit c207575) fires the four
		// lifecycle names whenever Bus is non-nil; the heartbeat ticks
		// gate separately on HeartbeatInterval, which stays zero here
		// because the CLI surface drops tick events by design.
		sdkOpts.Bus = bridgeAgentLoopEvents(opts, turn)
	}
	// Usage rows: the completer's onUsage callback (newSDKTurnCompleter)
	// runs l.emitTurnUsage per Chat call and writes the one token_usage
	// row the legacy loop writes; an Audit bridge would duplicate it.
	res, err := runSDKPromptTooLongRecoverable(ctx, l, sdkOpts, opts, preparedMsgs, turn)
	stampSDKToolMessageNames(res.History)
	if err != nil {
		// A canceled or timed-out run keeps the streamed partial the
		// tee emitted (recorded into l.Messages BEFORE the dispatcher's
		// history write-back; runOnceSDK preserves messages appended
		// past the turn's pre-append).
		recordSDKCanceledStreamPartial(ctx, l, turn, err)
		// Return the partial Result alongside the error: the SDK's
		// hard-fail Result carries the messages completed so far, and
		// the dispatcher writes them back so an errored turn keeps its
		// partial history, mirroring the legacy path.
		return res, err
	}
	// A surface-bridge failure (registry conversion at a mid-run
	// rotation) recorded in the turn state kept the prior surface so
	// the run could wind down gracefully; the turn still fails with
	// the recorded error, carried through the same partial-Result
	// path as a hard failure.
	if berr := turn.bridgeError(); berr != nil {
		return res, berr
	}
	return finishSDKResult(opts, res, msgs)
}

// ensureSDKDispatcher installs a scoped runtime dispatcher over the
// loop's registry when the caller wired none (the same construction
// the subagent handler uses); the returned func closes it.
func ensureSDKDispatcher(l *Loop, opts *Options) (func(), error) {
	if opts.Dispatcher != nil || l.Tools == nil {
		return func() {}, nil
	}
	d, err := runtime.NewToolDispatcher(l.Tools, runtime.Policy{})
	if err != nil {
		return nil, fmt.Errorf("agent: scoped tool dispatcher: %w", err)
	}
	opts.Dispatcher = d
	return d.Close, nil
}

// finishSDKTurn applies finalizeSDKTurn with the current turn's user
// text (the last message of the starting history) as the content-match
// boundary.
func finishSDKResult(opts Options, res sdkagentloop.Result, msgs []provider.Message) (sdkagentloop.Result, error) {
	turnUser := ""
	if n := len(msgs); n > 0 && msgs[n-1].Role == provider.RoleUser {
		turnUser = msgs[n-1].Content
	}
	if err := finalizeSDKTurn(opts, res, turnUser); err != nil {
		return res, err
	}
	return res, nil
}

// recordSDKCanceledStreamPartial keeps the streamed partial a canceled
// or timed-out SDK run already emitted through its StreamingWriter tee:
// the SDK's hard-fail Result never carries in-flight stream bytes, so
// recordInterruptedPartial (the real legacy method, with its own
// narrowness: non-blank text only) records them into l.Messages. Any
// other error is a fragment, not a turn, and records nothing.
func recordSDKCanceledStreamPartial(ctx context.Context, l *Loop, turn *sdkTurnState, err error) {
	if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
		return
	}
	if tee := turn.currentStreamTee(); tee != nil {
		l.recordInterruptedPartial(tee)
	}
}

// runSDKPromptTooLongRecoverable drives one SDK run and, when the
// provider rejects the prompt as too long, applies the LEGACY recovery
// from loop_recovery.go's retryAfterPromptTooLong once: compact the
// starting history to the same fixed 16K target (clamped to
// MaxContextTokens/4 when smaller), append the same model-visible
// compact notice, emit the same EventPrune, and retry the run exactly
// once on a freshly built SDK loop. A second rejection propagates
// unchanged (fail fast - no second retry, no loop). DisableProviderReplay
// suppresses the retry, mirroring the legacy gate: the retry IS a
// provider replay of the rejected call. Accepted gaps versus the legacy
// retry (documented, not wired here): the legacy path's retry-time
// summary re-derivation (refreshOmittedEvidenceAfterRetry, memo
// invalidation, injectSummary) and its prompt-token reservation are not
// reproduced; the SDK's own Window-based recovery stays disabled
// because the host wires no Window (that is a separate semantic item).
func runSDKPromptTooLongRecoverable(ctx context.Context, l *Loop, sdkOpts sdkagentloop.Options, opts Options, preparedMsgs []sdkshape.Message, turn *sdkTurnState) (sdkagentloop.Result, error) {
	run := func(msgs []sdkshape.Message) (sdkagentloop.Result, error) {
		loop, err := sdkagentloop.New(sdkOpts)
		if err != nil {
			return sdkagentloop.Result{}, err
		}
		return runSDKSteerable(ctx, loop, opts, msgs, turn)
	}
	res, err := run(preparedMsgs)
	if err == nil || opts.DisableProviderReplay ||
		(!errors.Is(err, provider.ErrPromptTooLong) && !errors.Is(err, sdkshape.ErrPromptTooLong)) {
		return res, err
	}
	return run(sdkCompactAfterPromptTooLong(l, opts, preparedMsgs))
}

// sdkCompactAfterPromptTooLong applies the legacy
// retryAfterPromptTooLong recovery shape to an SDK starting history:
// the fixed 16K target (clamped to MaxContextTokens/4 when smaller),
// the notice's own tokens charged against the same target,
// PruneMessagesKeepTurns keeps the system prompt and the newest turns
// and drops tool exchanges as a unit, and the model-visible notice is
// appended after the prune. One EventPrune announces the compaction
// with the same detail text the legacy path emits.
func sdkCompactAfterPromptTooLong(l *Loop, opts Options, msgs []sdkshape.Message) []sdkshape.Message {
	target := 16 << 10
	if opts.MaxContextTokens > 0 && opts.MaxContextTokens/4 < target {
		target = opts.MaxContextTokens / 4
	}
	if target < 1 {
		target = 1
	}
	notice := provider.Message{Role: provider.RoleUser, Content: promptTooLongCompactNotice}
	pruneTarget := target - provider.MessageTokens(notice)
	if pruneTarget < 1 {
		pruneTarget = 1
	}
	pruned := provider.PruneMessagesKeepTurns(sdkMessagesToCLI(msgs), pruneTarget, l.contextAccounting())
	pruned = append(pruned, notice)
	emit(opts, Event{
		Kind:   EventPrune,
		Detail: fmt.Sprintf("provider rejected prompt (prompt too long); compacted to %d tokens and retrying once", target),
	})
	return cliMessagesToSDK(pruned)
}

// signal bridge on one built SDK loop and drives RunSteerable.
func runSDKSteerable(ctx context.Context, loop *sdkagentloop.Loop, opts Options, preparedMsgs []sdkshape.Message, turn *sdkTurnState) (sdkagentloop.Result, error) {
	steer := sdkagentloop.NewSteer()
	// BeforeStep carrier (plan 54, blocker 2 of the SDK convergence):
	// install the legacy BeforeStep as the SDK's pull-based steer
	// injector. The SDK drains it at the top of every iteration AND
	// at every steered-stop decision point, exactly mirroring the
	// legacy context.go:15-19 placement. A nil opts.BeforeStep means
	// no injector is installed and the SDK's existing Trigger
	// semantics are unchanged.
	if opts.BeforeStep != nil {
		steer.SetInjector(func() []sdkshape.Message {
			return cliMessagesToSDK(opts.BeforeStep())
		})
	}
	// bridgeSteerSignals spawns at most three goroutines (InterruptCh,
	// MailboxPendingInterrupt poller, MailboxPending poller) and
	// selects each one on a run-scoped done channel, so the goroutines
	// exit when RunSteerable returns, not only when ctx is canceled.
	// A long-lived caller ctx no longer leaks two pollers per turn.
	runDone := make(chan struct{})
	defer close(runDone)
	bridgeSteerSignals(ctx, runDone, opts, steer)
	// Context-cancel hook for the host-side turn shaping cond: a
	// wrapper parked waiting on its predecessors would never wake if
	// nothing else broadcasts the cond on ctx cancel - sync.Cond.Wait
	// does not observe ctx.Done. abortIterationShaping closes the
	// counter's signal channel once and exits; the wrapper's select
	// fires on the closed channel and the goroutine returns. The
	// watcher itself exits when ctx cancels or the run finishes. A
	// nil turn means BatchResultBudgetBytes was zero and no cond was
	// built, so the watcher is a no-op.
	abortCtxWatch(ctx, runDone, turn)
	return loop.RunSteerable(ctx, preparedMsgs, steer)
}

// abortCtxWatch fires turn.abortIterationShaping once when ctx
// cancels during the run. A nil turn means the run did not build a
// shaping counter (BatchResultBudgetBytes == 0) and the watcher is a
// no-op. The runDone channel closes the watcher when RunSteerable
// returns, so the goroutine never outlives its run.
func abortCtxWatch(ctx context.Context, runDone <-chan struct{}, turn *sdkTurnState) {
	if turn == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			turn.abortIterationShaping()
		case <-runDone:
		}
	}()
}

// stampSDKToolMessageNames fills each RoleTool message's Name from the
// assistant ToolCall that requested it, mirroring the legacy
// processToolCalls (loop_tools.go:47-52): the SDK's RoleTool messages
// carry only ToolCallID, and CLI-side consumers (tests, history
// writers, message routing) match tool results by name.
func stampSDKToolMessageNames(history []sdkshape.Message) {
	names := make(map[string]string, len(history))
	for i := range history {
		m := &history[i]
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				names[tc.ID] = tc.Name
			}
		}
		if m.Role == sdkshape.RoleTool && m.Name == "" {
			m.Name = names[m.ToolCallID]
		}
	}
}

// sdkPromptBudgetPreflight mirrors the legacy prepareStep's
// PreparationManager-nil branch (context.go:20-23): when no
// preparation manager runs, MaxContextTokens is a hard preflight
// over the whole starting history plus tool schemas, before any
// completer call. A preparation manager owns the budget itself, so
// that path does no second check here either.
func sdkPromptBudgetPreflight(l *Loop, opts Options, msgs []provider.Message) error {
	if opts.PreparationManager == nil && opts.MaxContextTokens > 0 {
		return promptBudgetErrorWithTools(msgs, opts.MaxContextTokens, l.initialToolSpecs(opts), l.contextAccounting())
	}
	return nil
}

// finalizeSDKTurn applies the CLI's post-turn Options after a
// graceful SDK stop. FinalWriter receives the turn's final text: the
// final message's content, or - when the SDK zeroed Final (its
// documented behavior on StopMaxIterations, StopHookVeto, and
// StopSteered) - the last assistant text the turn produced anywhere,
// matching the legacy "no assistant text ANYWHERE" contract.
// RequireFinalText fails a turn that produced no assistant text in any
// step of the turn, except a steered stop, which the dispatcher maps
// to errSteerInterrupt instead.
func finalizeSDKTurn(opts Options, res sdkagentloop.Result, turnUserText string) error {
	text := res.Final.Content
	if strings.TrimSpace(text) == "" {
		// Content-match boundary, not an index bound: per-iteration
		// Trim compaction can shrink res.History below the turn's
		// starting length, so the backward scan starts at the current
		// turn's user message (sdkCurrentTurnStart's pattern) instead.
		startIdx := sdkCurrentTurnStart(res.History, turnUserText)
		for i := len(res.History) - 1; i >= startIdx; i-- {
			if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				text = m.Content
				break
			}
		}
	}
	// A non-empty res.Final streamed live to a FinalWriter through
	// the SDK StreamingWriter tee (see buildAgentLoopOptions), the
	// same rule the legacy commitFinalAnswer follows: rewriting it
	// here would duplicate the answer. A zero Final (StopMaxIterations
	// and the other pre-response stops) means the turn's last text
	// was revoked as optimistic content when its tool calls started,
	// so that text reaches the writer only here.
	if opts.FinalWriter != nil && text != "" && strings.TrimSpace(res.Final.Content) == "" {
		if _, err := io.WriteString(opts.FinalWriter, text); err != nil {
			return fmt.Errorf("agent: write final text: %w", err)
		}
	}
	if opts.RequireFinalText && strings.TrimSpace(text) == "" && res.Stop != sdkagentloop.StopSteered {
		return fmt.Errorf("agent: turn produced no assistant text")
	}
	// Publish the terminal EventAssistant so uiadapter sees text.end
	// even when no FinalWriter was wired. The StreamingWriter tee
	// publishes DELTAS (Detail: "delta"); this one publishes the
	// committed text.end, mirroring legacy commitFinalAnswer's
	// post-final emit (loop.go).
	if text != "" {
		emit(opts, Event{Kind: EventAssistant, Content: text})
	}
	return nil
}

// RunAgentLoop drives the SDK's agentloop.Loop for one Options. It
// requires a Loop carrying the completer and registry; a zero Loop
// fails closed at the nil-completer check in newAgentLoopCompleter.
func RunAgentLoop(ctx context.Context, l *Loop, opts Options) (sdkagentloop.Result, error) {
	return RunAgentLoopOnce(ctx, l, opts, nil)
}

// prepareSDKHistory applies the CLI's host-side preparation and
// summary-injection passes to the loop's history before the SDK
// runs. The SDK's Window stays nil so the SDK does not run its own
// per-iteration planning pass on top of the CLI's host-side
// compaction; the CLI's SummaryConfig.Summarizer runs host-side
// through Loop.injectSummary after a compacted outcome, and the SDK
// receives the rendered summary message as the last user-role
// frame in its starting history. The CLI's 7-field Summary stays
// authoritative on the wire (the SDK's 5-field schema is never
// reached). A nil PreparationManager returns the loop's history
// unchanged. See docs/development/sdk-backend-field-mapping.md for
// the full rationale.
// Compile-time check: SDK's Completer type is reachable from the
// adapter package through the same alias the bridge package uses.
var _ sdkshape.Completer
