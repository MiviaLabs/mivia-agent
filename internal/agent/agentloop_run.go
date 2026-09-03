// Package agent - SDK agent-loop run driver.
//
// RunAgentLoopOnce drives the SDK's mivia-ai-sdk/agentloop.Loop (built
// by agentloop_adapter.go's buildAgentLoopOptions) for one turn: it
// applies the prompt-budget preflight, bridges the steer signals, runs
// the prompt-too-long recovery retry, and returns the SDK Result. It
// is ADDITIVE: the legacy (*Loop).Run in loop.go is unchanged, and the
// dispatcher's "sdk" branch (loop_dispatch.go) chooses the runtime.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

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
	// The no-PreparationManager preflight runs BEFORE the initial
	// history is captured below: it prunes old turns to fit
	// MaxContextTokens first (matching the legacy prepareStep's
	// fallback branch), and the pruned slice - not the raw msgs this
	// call received - is what the rest of the turn, including
	// sdkInitialHistory, must see.
	msgs, err := sdkPromptBudgetPreflight(l, opts, msgs)
	if err != nil {
		return sdkagentloop.Result{}, err
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
	sdkOpts, turn, err := buildAgentLoopOptions(l, opts, lastUserText(msgs))
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	sdkOpts.Trim = sdkPrepareTrim(l, opts, turn)
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
		return handleSDKRunError(ctx, l, opts, turn, res, err)
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
	if err := finalizeSDKTurn(opts, res, lastUserText(msgs)); err != nil {
		return res, err
	}
	return res, nil
}

// lastUserText returns the content of msgs' final message when it is a user
// turn, else "". This is the content-match boundary sdkCurrentTurnStart uses
// to locate where the current turn begins in a Result's History - shared by
// finishSDKResult and retryOnEmptyResponse so both agree on the same turn
// boundary.
func lastUserText(msgs []provider.Message) string {
	if n := len(msgs); n > 0 && msgs[n-1].Role == provider.RoleUser {
		return msgs[n-1].Content
	}
	return ""
}

// recordSDKCanceledStreamPartial keeps the streamed partial a canceled
// or timed-out SDK run already emitted through its StreamingWriter tee:
// the SDK's hard-fail Result never carries in-flight stream bytes, so
// recordInterruptedPartial (the real legacy method, with its own
// narrowness: non-blank text only) records them into l.Messages. Any
// other error is a fragment, not a turn, and records nothing.
func recordSDKCanceledStreamPartial(ctx context.Context, l *Loop, turn *sdkTurnState, err error) {
	if !sdkErrIsInterrupted(ctx, err) {
		return
	}
	if tee := turn.currentStreamTee(); tee != nil {
		l.recordInterruptedPartial(tee)
	}
}

// sdkErrIsInterrupted reports whether err represents a canceled or
// timed-out run rather than a genuine hard failure. Shared by
// recordSDKCanceledStreamPartial and handleSDKRunError so both agree
// on the same interrupted/hard-failure split the legacy runStep made.
func sdkErrIsInterrupted(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

// handleSDKRunError applies RunAgentLoopOnce's post-run error handling:
// an interrupted run keeps the streamed partial the tee emitted
// (recorded into l.Messages BEFORE the dispatcher's history
// write-back). The loop retains any successfully-captured preparation
// on both interrupted and hard errors so the caller (finishErroredContextTurn
// or subagent runner) can decide whether to commit an OutcomeUpstreamErr
// checkpoint or discard it. The partial Result is returned alongside err:
// the SDK's hard-fail Result carries the messages completed so far, and the
// dispatcher writes them back so an errored turn keeps its partial history.
func handleSDKRunError(ctx context.Context, l *Loop, opts Options, turn *sdkTurnState, res sdkagentloop.Result, err error) (sdkagentloop.Result, error) {
	recordSDKCanceledStreamPartial(ctx, l, turn, err)
	return res, err
}

// maxEmptyResponseRetries bounds how many times the SDK loop's
// ContinueOnStop hook (continue_on_stop.go) silently re-drives a turn after
// a genuinely empty provider response (sdkagentloop.StopEmptyResponse: no
// text, no tool calls) before giving up and letting finalizeSDKTurn's
// RequireFinalText surface "agent: turn produced no assistant text" to the
// caller. An empty response is a known, often-transient provider failure
// mode (the server reports success with nothing generated) rather than a
// deterministic rejection, so a small bounded retry resolves the common
// case without the user needing to manually retype "continue" - which,
// before this retry existed, is also how a poisoned empty-assistant message
// could enter and re-poison history on every subsequent turn (see
// provider.DropEmptyAssistantTurns and chat.adoptFailedTurnSnapshot's
// validation guard for the persistence-side half of this fix).
const maxEmptyResponseRetries = 2

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
	//
	// wg.Wait() (deferred BEFORE close(runDone), so it runs AFTER it -
	// defers unwind LIFO) blocks this call from returning until every
	// bridge goroutine has actually exited, not just been signaled to.
	// This matters for runSDKPromptTooLongRecoverable's retry, which
	// calls runSDKSteerable a second time reusing the same
	// opts.InterruptCh()-returned channel: without the wait, the first
	// call's not-yet-scheduled goroutine and the retry's freshly
	// spawned one can both still be selecting on that channel, and the
	// stale one can win the race for a caller-sent interrupt - firing
	// a Steer nobody is listening to anymore while the retry hangs.
	var wg sync.WaitGroup
	defer wg.Wait()
	runDone := make(chan struct{})
	defer close(runDone)
	bridgeSteerSignals(ctx, runDone, opts, steer, &wg)
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

// finalizeSDKTurn applies the CLI's post-turn Options after a
// graceful SDK stop. FinalWriter receives the turn's final text: the
// final message's content, or - when the SDK zeroed Final (its
// documented behavior on StopMaxIterations, StopHookVeto,
// StopEmptyResponse, and StopSteered) - the last assistant text the
// turn produced anywhere, matching the legacy "no assistant text
// ANYWHERE" contract.
// RequireFinalText fails a turn that produced no assistant text in any
// step of the turn, except a steered stop, which the dispatcher maps
// to errSteerInterrupt instead.
func finalizeSDKTurn(opts Options, res sdkagentloop.Result, turnUserText string) error {
	text := sdkResolvedFinalText(res, turnUserText)
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

// sdkResolvedFinalText returns res.Final.Content when non-blank, else the
// most recent non-blank assistant message in res.History for the current
// turn (the same "no text ANYWHERE in the turn" fallback finalizeSDKTurn's
// RequireFinalText check enforces). Shared with retryOnEmptyResponse so a
// retry decision and the eventual pass/fail check agree on the exact same
// definition of "this turn produced nothing" - in particular, a turn whose
// FINAL step came back empty but which produced usable text earlier (a
// tool-call step's assistant content) must never be retried, since retrying
// discards that earlier text and replays the whole turn from scratch.
func sdkResolvedFinalText(res sdkagentloop.Result, turnUserText string) string {
	if text := strings.TrimSpace(res.Final.Content); text != "" {
		return res.Final.Content
	}
	// Content-match boundary, not an index bound: per-iteration Trim
	// compaction can shrink res.History below the turn's starting length,
	// so the backward scan starts at the current turn's user message
	// (sdkCurrentTurnStart's pattern) instead.
	startIdx := sdkCurrentTurnStart(res.History, turnUserText)
	for i := len(res.History) - 1; i >= startIdx; i-- {
		if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

// sdkTurnMadeToolCalls reports whether any message in the current turn's
// slice of res.History (from sdkCurrentTurnStart onward) carries a tool
// call. Used by retryOnEmptyResponse to refuse retrying an attempt that
// already dispatched a tool - see its doc comment for why a whole-turn
// replay after that point risks duplicating a non-idempotent side effect.
func sdkTurnMadeToolCalls(history []sdkshape.Message, turnUserText string) bool {
	startIdx := sdkCurrentTurnStart(history, turnUserText)
	for i := startIdx; i < len(history); i++ {
		if len(history[i].ToolCalls) > 0 {
			return true
		}
	}
	return false
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
