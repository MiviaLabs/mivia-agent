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
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/usage"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// defaultSDKMaxIterations backs MaxSteps when the caller leaves it
// unset. The legacy loop treats 0 as unbounded; the SDK's Validate
// requires a positive MaxIterations, so the adapter substitutes a
// finite default rather than fail-closing on the most common
// zero-value configuration.
const defaultSDKMaxIterations = 25

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
func buildAgentLoopOptions(l *Loop, opts Options) (sdkagentloop.Options, error) {
	if err := rejectUnsupportedSDKBatches(opts); err != nil {
		return sdkagentloop.Options{}, err
	}
	turn := newSDKTurnState()
	completer, err := newAgentLoopCompleterWithDefaults(l.Completer, turnRequestDefaults{
		reasoning:             opts.Reasoning,
		maxTokens:             opts.MaxTokens,
		temperature:           opts.Temperature,
		requestTimeout:        opts.RequestTimeout,
		disableProviderReplay: opts.DisableProviderReplay,
		sessionID:             opts.SessionID,
	}, func(finishReason string) { l.LastFinishReason = finishReason }, func() { turn.steps.Add(1) })
	if err != nil {
		return sdkagentloop.Options{}, err
	}
	sdkTools, err := sdkadapter.ConvertToolRegistryWithAdmission(l.Tools, sdkadapter.AdmissionPredicates{
		StagedMessage:     opts.StagedToolMessage,
		UnadmittedHandler: opts.UnadmittedToolHandler,
	})
	if err != nil {
		return sdkagentloop.Options{}, err
	}
	applyDispatcherShim(sdkTools, l.Tools, opts, turn)
	applyRefOnlyShim(sdkTools, l.Tools, opts.RefOnlyTools, opts.RemainderSpool, BatchDegradeFloorBytes, opts.SessionID)
	// Host-side turn shaping replaces the SDK's TurnResultBudget: the
	// CLI contract degrades with an honest notice and never omits a
	// call, so out.TurnResultBudget stays unset below.
	applyTurnShaping(sdkTools, l.Tools, opts, turn)
	maxIterations := opts.MaxSteps
	if maxIterations <= 0 {
		maxIterations = defaultSDKMaxIterations
	}
	out := sdkagentloop.Options{
		Completer:       completer,
		Tools:           sdkTools,
		Model:           opts.Model,
		MaxIterations:   maxIterations,
		MaxCallsPerTurn: opts.MaxToolCallsPerBatch,
		SessionID:       opts.SessionID,
	}
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
	// WatchdogInterval deliberately does NOT map to
	// HeartbeatInterval: a positive HeartbeatInterval requires a Bus
	// the CLI path does not wire, and Validate would reject the
	// options. The watchdog's steer-latency role is carried by the
	// MailboxPending poller in the steer bridge instead.
	return out, nil
}

// rejectUnsupportedSDKBatches fails closed on every CLI Options field
// whose semantics the SDK path cannot carry. Zero values pass: a
// caller that never set the knob loses nothing by switching backends.
//
// Fields the SDK accepts at zero but interprets differently are NOT
// rejected here: the accepted-semantic-gap table lives on the
// agentloop adapter's package doc. Today those are MaxConcurrentTools
// (the SDK runs tool calls sequentially within a turn, ordered by
// ToolCall.Index) and a negative BatchResultBudgetBytes (the SDK's
// TurnResultBudget is a literal byte budget only, not the CLI's
// "derived from MaxContextTokens" mode). Both pass through to the SDK
// silently; the CLI caller accepts the difference.
func rejectUnsupportedSDKBatches(opts Options) error {
	if opts.Surface != nil {
		return unsupportedSDKOption("Surface")
	}
	// BeforeStep is carried via the Steer injector installed in
	// RunAgentLoopOnce: RunAgentLoopOnce calls opts.BeforeStep on
	// the loop goroutine at the top of each iteration (after this
	// reject ran) and at every steered-stop downgrade point, then
	// converts the returned CLI messages to SDK shape and appends
	// them to history. The SDK-side carrier is
	// agentloop/steer.go's SetInjector; see the carrier cell in
	// docs/development/sdk-backend-field-mapping.md for the
	// placement, ordering, and gap table.
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
	// in RunAgentLoopOnce); the four token-reservation fields have no
	// SDK analogue - the SDK loop reserves nothing and refunds
	// nothing - so each fails closed by name.
	if opts.WorkLimits.MaxPromptTokens > 0 {
		return unsupportedSDKOption("WorkLimits.MaxPromptTokens")
	}
	if opts.WorkLimits.MaxOutputTokens > 0 {
		return unsupportedSDKOption("WorkLimits.MaxOutputTokens")
	}
	if opts.WorkLimits.MaxOutputPerCall > 0 {
		return unsupportedSDKOption("WorkLimits.MaxOutputPerCall")
	}
	if opts.WorkLimits.MaxToolCalls > 0 {
		return unsupportedSDKOption("WorkLimits.MaxToolCalls")
	}
	// PreserveWorkLimits passes: the flag only preserves the four
	// token-reservation counters, and each of those already fails
	// closed by name above. With them all zero the legacy meter is
	// inert, so rejecting the flag alone would fail-close a no-op.
	// MaxContextTokens, OnEvent, EventBus, UsageWriter, FinalWriter,
	// RequireFinalText, SummaryConfig.Summarizer, StagedToolMessage,
	// UnadmittedToolHandler, RefOnlyTools, RemainderSpool, Dispatcher,
	// MaxToolResultChars, ToolTimeout, Reasoning, MaxTokens,
	// Temperature, RequestTimeout, DisableProviderReplay, and
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
	// MaxContextTokens is honored by pre-compacting the loop's history
	// through opts.PreparationManager before handing the messages to
	// the SDK. The SDK's Window stays nil so the SDK does not run its
	// own per-iteration planning pass on top of the CLI's host-side
	// compaction. A nil PreparationManager keeps the loop's history
	// unchanged; the SDK's per-call Budget still bounds one Completer
	// call's messages by byte count after the fact.
	preparedMsgs, err := prepareSDKHistory(ctx, l, opts, msgs)
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
	if opts.Dispatcher == nil && l.Tools != nil {
		d, derr := runtime.NewToolDispatcher(l.Tools, runtime.Policy{})
		if derr != nil {
			return sdkagentloop.Result{}, fmt.Errorf("agent: scoped tool dispatcher: %w", derr)
		}
		opts.Dispatcher = d
		defer d.Close()
	}
	sdkOpts, err := buildAgentLoopOptions(l, opts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	if opts.OnEvent != nil || opts.EventBus != nil {
		// The SDK (since mivia-ai-sdk commit c207575) fires the four
		// lifecycle names whenever Bus is non-nil; the heartbeat ticks
		// gate separately on HeartbeatInterval, which stays zero here
		// because the CLI surface drops tick events by design.
		sdkOpts.Bus = bridgeAgentLoopEvents(opts)
	}
	if opts.UsageWriter != nil {
		sdkOpts.Audit = bridgeUsageAudit(opts, l.Completer.Name())
	}
	loop, err := sdkagentloop.New(sdkOpts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	res, err := runSDKSteerable(ctx, loop, opts, preparedMsgs)
	stampSDKToolMessageNames(res.History)
	if err != nil {
		// Return the partial Result alongside the error: the SDK's
		// hard-fail Result carries the messages completed so far, and
		// the dispatcher writes them back so an errored turn keeps its
		// partial history, mirroring the legacy path.
		return res, err
	}
	if err := finalizeSDKTurn(opts, res, len(preparedMsgs)); err != nil {
		return res, err
	}
	return res, nil
}

// runSDKSteerable installs the BeforeStep injector and the steer
// signal bridge on one built SDK loop and drives RunSteerable.
func runSDKSteerable(ctx context.Context, loop *sdkagentloop.Loop, opts Options, preparedMsgs []sdkshape.Message) (sdkagentloop.Result, error) {
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
	return loop.RunSteerable(ctx, preparedMsgs, steer)
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

// bridgeUsageAudit adapts the CLI's durable usage writer onto the
// SDK's per-event audit callback. One completed completion yields one
// token_usage row with the provider-reported actuals, the same shape
// the legacy path's EmitTokenUsage writes; estimate and calibration
// fields stay zero because the SDK reports actuals only. A non-nil
// return from an AuditFunc is a hard run failure, and usage writes
// are best-effort by contract, so the bridge always returns nil.
func bridgeUsageAudit(opts Options, providerName string) sdkagentloop.AuditFunc {
	return func(ctx context.Context, rec sdkagentloop.AuditRecord) error {
		if rec.Kind != sdkagentloop.AuditKindCompletion {
			return nil
		}
		u := rec.Response.Usage
		if u.PromptTokens == 0 && u.CompletionTokens == 0 {
			return nil
		}
		recordUsage(ctx, opts, usage.UsageRecord{
			Kind:         "token_usage",
			Provider:     providerName,
			Model:        rec.Request.Model,
			InputTokens:  u.PromptTokens,
			OutputTokens: u.CompletionTokens,
		})
		return nil
	}
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
func finalizeSDKTurn(opts Options, res sdkagentloop.Result, startLen int) error {
	text := res.Final.Content
	if strings.TrimSpace(text) == "" {
		for i := len(res.History) - 1; i >= startLen; i-- {
			if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				text = m.Content
				break
			}
		}
	}
	if opts.FinalWriter != nil && text != "" {
		if _, err := io.WriteString(opts.FinalWriter, text); err != nil {
			return fmt.Errorf("agent: write final text: %w", err)
		}
	}
	if opts.RequireFinalText && strings.TrimSpace(text) == "" && res.Stop != sdkagentloop.StopSteered {
		return fmt.Errorf("agent: turn produced no assistant text")
	}
	return nil
}

// RunAgentLoop drives the SDK's agentloop.Loop for one Options. It
// requires a Loop carrying the completer and registry; a zero Loop
// fails closed at the nil-completer check in newAgentLoopCompleter.
func RunAgentLoop(ctx context.Context, l *Loop, opts Options) (sdkagentloop.Result, error) {
	return RunAgentLoopOnce(ctx, l, opts, nil)
}

// bridgeSteerSignals wires the CLI's interrupt signals onto one Steer
// handle. Each non-nil signal spawns one goroutine; all exit when
// runDone closes (i.e. RunSteerable returns) OR ctx is canceled,
// whichever comes first, so a long-lived caller ctx leaks no pollers.
//
// The three signal sources model the CLI's three cancellation layers:
//   - InterruptCh resolves once per fire and, when MailboxPendingInterrupt
//     is also set, fires Trigger ONLY when an Interrupt-flagged steer
//     is queued. When MailboxPendingInterrupt is nil, InterruptCh fires
//     Trigger unconditionally — a bare InterruptCh with no mailbox gate
//     is an explicit interrupt (the legacy "fire on close" semantics).
//   - MailboxPendingInterrupt is the strict signal-branch poller:
//     the predicate reports whether an Interrupt-flagged steer is
//     queued. Trigger fires the moment it returns true.
//   - MailboxPending is the loose watchdog poller (gated on
//     WatchdogInterval > 0): the predicate reports whether ANY message
//     is waiting, so a stale signal after a drain can never cancel a call.
//
// All three sites share one SoftInterruptCooldown gate. A positive
// cooldown caps Trigger fires to one per window; a zero cooldown
// disables the gate, mirroring the legacy steerCooldownOK semantics.
// The shared cooldownUntil is intra-RunAgentLoopOnce only (a local
// atomic.Int64 here), so the gate does not span multiple SDK turns;
// the legacy's cross-call gate (Loop.softInterruptAt) is not portable
// to the SDK's per-RunSteerable Steer value and is recorded as an
// accepted semantic gap.
func bridgeSteerSignals(ctx context.Context, runDone <-chan struct{}, opts Options, steer *sdkagentloop.Steer) {
	var cooldownUntil atomic.Int64
	cooldownOK := func() bool {
		if opts.SoftInterruptCooldown <= 0 {
			return true
		}
		return time.Now().UnixNano() >= cooldownUntil.Load()
	}
	noteFire := func() {
		if opts.SoftInterruptCooldown <= 0 {
			return
		}
		cooldownUntil.Store(time.Now().UnixNano() + int64(opts.SoftInterruptCooldown))
	}
	fireSteer := func() {
		if !cooldownOK() {
			return
		}
		noteFire()
		steer.Trigger()
	}
	if ch := opts.InterruptCh; ch != nil {
		interrupt := opts.MailboxPendingInterrupt
		go func() {
			select {
			case <-ch():
				if interrupt == nil {
					fireSteer()
					return
				}
				if interrupt() {
					fireSteer()
				}
				// An Interrupt-flagged steer not yet queued: drain
				// the stale signal without firing. The strict
				// watchdog poller below will catch the
				// queued-and-flagged case.
			case <-runDone:
			case <-ctx.Done():
			}
		}()
	}
	pollInterval := opts.WatchdogInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	if interrupt := opts.MailboxPendingInterrupt; interrupt != nil {
		go func() {
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-runDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					// A trigger fired when no Completer.Chat call is
					// in flight sets the SDK's per-RunSteerable
					// trigger flag; the next arm observes it and
					// immediately cancels the next chat, the bridge
					// fires again, the next chat cancels, and the
					// run never makes progress. Guard on
					// HasActiveCall so bridge triggers fire only
					// when there is a live chat to cancel. A
					// pre-chat trigger that the bridge intentionally
					// means to "save for the next arm" is the
					// legacy semantics; the bridge mirrors the
					// legacy per-call watcher and must observe the
					// same gate.
					if interrupt() && steer.HasActiveCall() {
						fireSteer()
					}
				}
			}
		}()
	}
	// The loose watchdog poller mirrors the legacy SteerWatchdog: 0
	// disables it, so a non-urgent steer to a child configured without
	// a watchdog waits for the step boundary instead of canceling the
	// in-flight call (TestSteerLandsAtStepBoundaryUnchanged). A
	// positive interval keeps the legacy steer-latency bound.
	if pending := opts.MailboxPending; pending != nil && opts.WatchdogInterval > 0 {
		go func() {
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-runDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if pending() {
						fireSteer()
					}
				}
			}
		}()
	}
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
func prepareSDKHistory(ctx context.Context, l *Loop, opts Options, msgs []provider.Message) ([]sdkshape.Message, error) {
	if opts.PreparationManager == nil {
		return cliMessagesToSDK(msgs), nil
	}
	input := l.buildPrepareInput(nil, opts)
	input.Messages = msgs
	preparation, err := opts.PreparationManager.Prepare(ctx, input)
	if err != nil {
		// Match context.go:27-39's fallback: an interrupted ctx on a
		// fresh attempt with no recorded preparation retries once with
		// context.Background so the run still produces a compacted
		// history to ship downstream.
		if !opts.WorkLimits.DeadlineAt.IsZero() && interruptedContext(ctx, err) {
			if fallback, ferr := opts.PreparationManager.Prepare(context.Background(), input); ferr == nil {
				l.recordPreparation(fallback)
				l.captureOmittedEvidence(input, fallback)
				return cliMessagesToSDK(clonePreparedMessages(injectSummaryAfterPrepare(l, ctx, opts, fallback.Messages))), nil
			} else {
				return nil, ferr
			}
		}
		return nil, err
	}
	l.recordPreparation(preparation)
	l.captureOmittedEvidence(input, preparation)
	return cliMessagesToSDK(clonePreparedMessages(injectSummaryAfterPrepare(l, ctx, opts, preparation.Messages))), nil
}

// injectSummaryAfterPrepare runs the CLI's host-side summary inject
// on a freshly prepared messages slice and returns the slice with the
// summary message appended as the last user-role frame. A nil
// summarizer or a non-compacted preparation returns the messages
// unchanged, mirroring injectSummary's structural-only fallback.
func injectSummaryAfterPrepare(l *Loop, ctx context.Context, opts Options, prepared []provider.Message) []provider.Message {
	if opts.SummaryConfig.Summarizer == nil || !l.HasPreparation || !l.LastPreparation.Compacted {
		return prepared
	}
	// Temporarily swap l.Messages to the prepared slice so injectSummary
	// reads the compacted history instead of the live one, then restore.
	// This matches what loop.go:332 does in the legacy path: prepareStep
	// overwrites l.Messages from preparation.Messages before
	// injectSummary sees them.
	original := l.Messages
	l.Messages = prepared
	defer func() { l.Messages = original }()
	return l.injectSummary(ctx, opts)
}

// Compile-time check: SDK's Completer type is reachable from the
// adapter package through the same alias the bridge package uses.
var _ sdkshape.Completer
