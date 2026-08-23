// Package agent - SDK agent-loop adapter.
//
// RunAgentLoopOnce drives the SDK's mivia-ai-sdk/agentloop.Loop for
// one turn: it converts the CLI registry, wraps the CLI completer,
// bridges the steer signals, and returns the SDK Result. It is
// ADDITIVE: the legacy (*Loop).Run in loop.go is unchanged, and the
// dispatcher's "sdk" branch stays stubbed until commit 4 wires it.
//
// The field mapping below is deliberately partial. Every CLI Options
// field whose semantics the SDK path cannot yet carry fails closed
// with an error naming the field, so an opt-in caller learns the
// boundary at the call instead of silently losing behavior. The
// fail-closed set shrinks as later commits wire event translation,
// context planning, and the batch-budget derivation.
//
// The SDK imports are out-of-prefix; the gate filters them out of
// the in-prefix edge set (scripts/check_import_layers.py
// compute_edges), and the policy baseline is unchanged.
package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
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
	completer, err := newAgentLoopCompleter(l.Completer)
	if err != nil {
		return sdkagentloop.Options{}, err
	}
	sdkTools, err := sdkadapter.ConvertToolRegistry(l.Tools)
	if err != nil {
		return sdkagentloop.Options{}, err
	}
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
	// BatchResultBudgetBytes bounds one batch's summed result bytes;
	// the SDK's TurnResultBudget bounds one turn's summed result
	// bytes - the closest single analogue the SDK carries. The
	// negative derived-budget mode has no SDK analogue and was
	// rejected above.
	if opts.BatchResultBudgetBytes > 0 {
		out.TurnResultBudget = opts.BatchResultBudgetBytes
	}
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
	if opts.BeforeStep != nil {
		return unsupportedSDKOption("BeforeStep")
	}
	if opts.StagedToolMessage != nil {
		return unsupportedSDKOption("StagedToolMessage")
	}
	if opts.UnadmittedToolHandler != nil {
		return unsupportedSDKOption("UnadmittedToolHandler")
	}
	if len(opts.RefOnlyTools) > 0 {
		return unsupportedSDKOption("RefOnlyTools")
	}
	if opts.RemainderSpool != nil {
		return unsupportedSDKOption("RemainderSpool")
	}
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
	if opts.PreserveWorkLimits {
		return unsupportedSDKOption("PreserveWorkLimits")
	}
	if opts.MaxContextTokens > 0 {
		return unsupportedSDKOption("MaxContextTokens")
	}
	// OnEvent, EventBus, UsageWriter, FinalWriter, RequireFinalText,
	// and MailboxPendingInterrupt are carried: the event bridge
	// translates SDK loop events, the audit bridge writes durable
	// token-usage rows per completed completion, finalizeSDKTurn
	// writes the final text and enforces the empty-turn refusal after
	// the run, and the steer bridge spawns a third goroutine that
	// polls MailboxPendingInterrupt as the strict signal branch
	// (parallel to the InterruptCh one-shot and the MailboxPending
	// watchdog poller).
	if opts.SummaryConfig.Summarizer != nil {
		return unsupportedSDKOption("SummaryConfig.Summarizer")
	}
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
	steer := sdkagentloop.NewSteer()
	bridgeSteerSignals(ctx, opts, steer)
	res, err := loop.RunSteerable(ctx, cliMessagesToSDK(msgs), steer)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	if err := finalizeSDKTurn(opts, res); err != nil {
		return sdkagentloop.Result{}, err
	}
	return res, nil
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
// graceful SDK stop: FinalWriter receives the final assistant text
// (post-hoc rather than streamed - the SDK result is whole), and
// RequireFinalText fails a turn that produced no assistant text
// anywhere, matching the legacy empty-turn refusal.
func finalizeSDKTurn(opts Options, res sdkagentloop.Result) error {
	if opts.FinalWriter != nil && res.Final.Content != "" {
		if _, err := io.WriteString(opts.FinalWriter, res.Final.Content); err != nil {
			return fmt.Errorf("agent: write final text: %w", err)
		}
	}
	if opts.RequireFinalText && strings.TrimSpace(res.Final.Content) == "" {
		return fmt.Errorf("agent: turn produced no assistant text")
	}
	return nil
}

// RunAgentLoop drives the SDK's agentloop.Loop for one Options. It
// delegates to RunAgentLoopOnce with a Loop carrying only the
// completer and registry the opts imply; the legacy (*Loop).Run in
// loop.go is NOT replaced. Kept for the B.2 #8 minimum-viable commit's
// exported surface; the dispatcher (commit 4) calls RunAgentLoopOnce.
func RunAgentLoop(ctx context.Context, opts Options) (sdkagentloop.Result, error) {
	l := &Loop{Completer: nil, Tools: nil}
	return RunAgentLoopOnce(ctx, l, opts, nil)
}

// bridgeSteerSignals wires the CLI's interrupt signals onto one Steer
// handle. Each non-nil signal spawns one goroutine; all exit on
// ctx.Done. Trigger fires at most once per signal source because the
// Steer resets its own state at RunSteerable start.
//
// The three signal sources model the CLI's three cancellation layers:
//   - InterruptCh resolves once and fires Trigger (one-shot signal).
//   - MailboxPendingInterrupt gates the strict signal branch: the
//     predicate reports whether an Interrupt-flagged steer is queued.
//     The goroutine polls at the watcher's poll interval (or 250ms
//     when unset) because the predicate is just a flag read.
//   - MailboxPending is the loose watchdog branch: the predicate
//     reports whether ANY message is waiting, so a stale signal after
//     a drain can never cancel a call.
func bridgeSteerSignals(ctx context.Context, opts Options, steer *sdkagentloop.Steer) {
	if ch := opts.InterruptCh; ch != nil {
		go func() {
			select {
			case <-ch():
				steer.Trigger()
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
				case <-ctx.Done():
					return
				case <-ticker.C:
					if interrupt() {
						steer.Trigger()
						return
					}
				}
			}
		}()
	}
	if pending := opts.MailboxPending; pending != nil {
		go func() {
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if pending() {
						steer.Trigger()
						return
					}
				}
			}
		}()
	}
}

// Compile-time check: SDK's Completer type is reachable from the
// adapter package through the same alias the bridge package uses.
var _ sdkshape.Completer
