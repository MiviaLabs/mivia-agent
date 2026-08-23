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
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
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
func rejectUnsupportedSDKBatches(opts Options) error {
	if opts.MaxConcurrentTools > 1 {
		return unsupportedSDKOption("MaxConcurrentTools")
	}
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
	if opts.BatchResultBudgetBytes < 0 {
		return unsupportedSDKOption("BatchResultBudgetBytes (negative derived mode)")
	}
	if opts.WorkLimits != (runtime.WorkLimits{}) {
		return unsupportedSDKOption("WorkLimits")
	}
	if opts.PreserveWorkLimits {
		return unsupportedSDKOption("PreserveWorkLimits")
	}
	if opts.RequireFinalText {
		return unsupportedSDKOption("RequireFinalText")
	}
	if opts.MaxContextTokens > 0 {
		return unsupportedSDKOption("MaxContextTokens")
	}
	if opts.MailboxPendingInterrupt != nil {
		return unsupportedSDKOption("MailboxPendingInterrupt")
	}
	if opts.OnEvent != nil {
		return unsupportedSDKOption("OnEvent")
	}
	if opts.EventBus != nil {
		return unsupportedSDKOption("EventBus")
	}
	if opts.UsageWriter != nil {
		return unsupportedSDKOption("UsageWriter")
	}
	if opts.FinalWriter != nil {
		return unsupportedSDKOption("FinalWriter")
	}
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
	sdkOpts, err := buildAgentLoopOptions(l, opts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	loop, err := sdkagentloop.New(sdkOpts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	steer := sdkagentloop.NewSteer()
	bridgeSteerSignals(ctx, opts, steer)
	return loop.RunSteerable(ctx, cliMessagesToSDK(msgs), steer)
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
// handle. Each non-nil signal spawns one goroutine; both exit on
// ctx.Done. Trigger fires at most once per signal source because the
// Steer resets its own state at RunSteerable start.
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
	if pending := opts.MailboxPending; pending != nil {
		interval := opts.WatchdogInterval
		if interval <= 0 {
			interval = 250 * time.Millisecond
		}
		go func() {
			ticker := time.NewTicker(interval)
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
