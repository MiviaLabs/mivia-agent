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

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
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
	sdkOpts, turn, err := buildAgentLoopOptions(l, opts)
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
	res, err := runSDKWithReplays(ctx, l, sdkOpts, opts, preparedMsgs, turn, lastUserText(msgs))
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

// runSDKWithReplays drives the run and then applies the two bounded
// host-side replays, in order. Both re-run the whole SDK loop, so both live
// here rather than in the caller: retryOnEmptyResponse covers a turn with
// no text and no tool calls, continueUnactedTurn the neighbouring turn with
// real text and no tool calls. Their preconditions are disjoint (one
// requires empty final text, the other non-empty), so at most one fires,
// and both are no-ops unless their own gate is set.
func runSDKWithReplays(ctx context.Context, l *Loop, sdkOpts sdkagentloop.Options, opts Options, preparedMsgs []sdkshape.Message, turn *sdkTurnState, turnUserText string) (sdkagentloop.Result, error) {
	res, err := runSDKPromptTooLongRecoverable(ctx, l, sdkOpts, opts, preparedMsgs, turn)
	res, err = retryOnEmptyResponse(ctx, l, sdkOpts, opts, preparedMsgs, turn, turnUserText, res, err)
	return continueUnactedTurn(ctx, l, sdkOpts, opts, turn, turnUserText, res, err)
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

// maxEmptyResponseRetries bounds how many times retryOnEmptyResponse
// silently re-runs the SDK loop after a genuinely empty provider response
// (sdkagentloop.StopEmptyResponse: no text, no tool calls) before giving up
// and letting finalizeSDKTurn's RequireFinalText surface "agent: turn
// produced no assistant text" to the caller. An empty response is a known,
// often-transient provider failure mode (the server reports success with
// nothing generated) rather than a deterministic rejection, so a small
// bounded retry resolves the common case without the user needing to
// manually retype "continue" - which, before this retry existed, is also
// how a poisoned empty-assistant message could enter and re-poison history
// on every subsequent turn (see provider.DropEmptyAssistantTurns and
// chat.adoptFailedTurnSnapshot's validation guard for the persistence-side
// half of this fix).
const maxEmptyResponseRetries = 2

// retryOnEmptyResponse re-runs the SDK loop from the same preparedMsgs, up
// to maxEmptyResponseRetries times, but ONLY when ALL hold: res.Stop is
// exactly StopEmptyResponse (not StopMaxIterations or any other stop);
// opts.RequireFinalText is set (a tolerant caller, e.g. a sub-agent, must
// not pay for a retry it never asked for); sdkResolvedFinalText is
// genuinely empty (a turn with usable earlier text, e.g. from a tool-call
// step, is never retried - that would discard it for no benefit);
// sdkTurnMadeToolCalls is false for the whole failed attempt, not just the
// triggering response; and l.LastFinishReason is not
// provider.FinishReasonRefusal - a refusal (Anthropic: HTTP 200, empty
// content) is structurally identical to "returned nothing" at the
// StopEmptyResponse layer, which never inspects FinishReason, so without
// this guard a refusal would retry against the same safety classifier for
// no benefit. See provider.FinishReasonRefusal's doc comment for why.
//
// The tool-call check above is load-bearing, not defense-in-depth: StopEmptyResponse
// only guarantees the triggering response made zero tool calls, not that
// the whole multi-iteration attempt did. A retry replays the ENTIRE turn
// from preparedMsgs (the pre-turn history), discarding any record that an
// earlier iteration's tool call already ran, so the model can and does
// reissue it - silently duplicating a non-idempotent side effect (a write,
// a command, an outbound call). A turn that already dispatched a tool
// hard-fails instead of risking that.
//
// preparedMsgs is never mutated by a run - agentloop.Loop.run defensively
// copies its input - so retrying against the same slice is safe.
// DisableProviderReplay suppresses this retry for the same reason it
// suppresses the prompt-too-long one: it IS a provider replay.
func retryOnEmptyResponse(ctx context.Context, l *Loop, sdkOpts sdkagentloop.Options, opts Options, preparedMsgs []sdkshape.Message, turn *sdkTurnState, turnUserText string, res sdkagentloop.Result, err error) (sdkagentloop.Result, error) {
	if !opts.RequireFinalText || opts.DisableProviderReplay {
		return res, err
	}
	shouldRetry := func() bool {
		return err == nil && res.Stop == sdkagentloop.StopEmptyResponse &&
			strings.TrimSpace(sdkResolvedFinalText(res, turnUserText)) == "" &&
			!sdkTurnMadeToolCalls(res.History, turnUserText) &&
			l.LastFinishReason != provider.FinishReasonRefusal
	}
	for attempt := 0; attempt < maxEmptyResponseRetries && shouldRetry(); attempt++ {
		// Announce the retry that is ABOUT to happen through the same emit
		// seam sdkPromptBudgetPreflight uses above, BEFORE re-running the
		// whole SDK loop, so a caller watching events never sits silent
		// while a second (or third) potentially multi-minute LLM call runs
		// behind what looked like a finished (if empty) turn. Purely
		// observability: it does not touch shouldRetry or the loop bound.
		emit(opts, Event{
			Kind:   EventEmptyResponseRetry,
			Detail: fmt.Sprintf("empty response on attempt %d/%d, retrying...", attempt+1, maxEmptyResponseRetries+1),
		})
		// Defense-in-depth for a narrow streaming edge case: the empty-response
		// case normally streams zero bytes (there is no content to forward),
		// but a provider that streams whitespace-only content before the
		// trimmed result reads as empty would have already forwarded those
		// bytes live to opts.FinalWriter through the shared teeWriter (the
		// same instance is reused across every retry attempt, installed once
		// in buildAgentLoopOptions before this loop runs). Revoking here
		// clears any such orphaned optimistic content before the fresh
		// attempt's answer streams in, the same way sdk_tool_events.go
		// already revokes on a tool-call arrival - a no-op when FinalWriter
		// doesn't implement streamRevoker or nothing was ever streamed.
		revokeStreamWriter(opts.FinalWriter)
		res, err = runSDKPromptTooLongRecoverable(ctx, l, sdkOpts, opts, preparedMsgs, turn)
	}
	return res, err
}

// promptTooLongCompactNotice is the model-visible notice appended to
// history after a prompt-too-long rejection compacts it. Dropping
// earlier tool results with only an operator EventPrune silently
// desynchronises the transcript from what the model can see: the next
// step would re-derive or re-read findings with no way to know they
// are gone. RoleUser keeps ValidateToolPairing happy (RoleSystem is
// only valid at index 0) and matches how the loop injects
// step-boundary notes (BeforeStep frames are user-role).
const promptTooLongCompactNotice = "[context compacted: the provider rejected the prompt as too long, so earlier turns and tool results were dropped to fit the model context; re-read any needed file with offset/limit for the remaining parts]"

// sdkCompactAfterPromptTooLong compacts an SDK starting history after a
// prompt-too-long rejection: the fixed 16K target (clamped to MaxContextTokens/4 when smaller),
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

// sdkPromptBudgetPreflight mirrors the legacy prepareStep's
// PreparationManager-nil branch (context.go:20-23) in full, not just
// its final rejection: when no preparation manager runs,
// MaxContextTokens is a hard preflight over the whole starting
// history plus tool schemas, before any completer call - but the
// legacy path pruned old turns to fit FIRST (pruneHistory) and only
// rejected what pruning could not fix. A preparation-manager-less
// caller with a real MaxContextTokens budget is not a rare edge case:
// every subagent/workflow loop that does not wire a
// ContextPreparationManager (optional; MaxContextTokens is not) hits
// this path, and skipping the prune would spuriously reject turns the
// legacy path pruned and admitted. Returns the (possibly pruned)
// messages the rest of the turn must use; the caller assigns them
// back over its own msgs so sdkInitialHistory sees the pruned form.
// A preparation manager owns the budget itself, so that path does no
// check here either.
func sdkPromptBudgetPreflight(l *Loop, opts Options, msgs []provider.Message) ([]provider.Message, error) {
	if opts.PreparationManager != nil || opts.MaxContextTokens <= 0 {
		return msgs, nil
	}
	toolSpecs := l.initialToolSpecs(opts)
	profile := l.contextAccounting()
	beforeTokens := provider.MessagesTokens(msgs, profile)
	pruned := sdkPruneToBudget(msgs, opts.MaxContextTokens, toolSpecs, profile)
	l.Messages = pruned
	if afterTokens := provider.MessagesTokens(pruned, profile); afterTokens < beforeTokens {
		emit(opts, Event{
			Kind:   EventPrune,
			Detail: fmt.Sprintf("pruned ~%d tokens (before=%d after=%d budget=%d)", beforeTokens-afterTokens, beforeTokens, afterTokens, opts.MaxContextTokens),
		})
	}
	if err := promptBudgetErrorWithTools(pruned, opts.MaxContextTokens, toolSpecs, profile); err != nil {
		return nil, err
	}
	return pruned, nil
}

// sdkPruneToBudget is the legacy pruneHistory's trim logic (deleted
// with the legacy engine), ported to return a new slice instead of
// mutating l.Messages directly - the only caller here already assigns
// the result onto l.Messages itself, once, at its one call site.
// Hysteretic like contextmgr.Plan: only prunes once the estimated
// request cost crosses the 80% trigger, down to the 50% target when
// it does, so the front-dropped prefix - and the provider
// prompt-cache miss it costs - happens once per many turns instead of
// every time the budget is merely close.
func sdkPruneToBudget(messages []provider.Message, maxContextTokens int, toolSpecs []provider.ToolSpec, profile provider.ContextAccountingProfile) []provider.Message {
	schemaCost := 0
	if len(toolSpecs) > 0 {
		if cost, err := provider.EstimateToolSchemaCost(toolSpecs); err == nil {
			schemaCost = cost
		}
	}
	trigger := contextmgr.PercentFloor(maxContextTokens, 4, 5)
	totalCost := provider.EstimateMessagesPromptCost(messages, schemaCost, profile)
	if totalCost < trigger {
		return messages
	}
	budget := contextmgr.PercentFloor(maxContextTokens, 1, 2)
	// Pass 1 drops old turns by content minus schema cost; pass 2 trims to
	// the exact frame- and schema-aware target of the remaining set -
	// mirrors the rejection check's own accounting so a history at the
	// boundary is trimmed instead of rejected.
	pass1 := budget - schemaCost
	if pass1 < 1 {
		pass1 = 1
	}
	pruned := provider.PruneMessagesKeepTurns(messages, pass1, profile)
	overhead := provider.EstimateMessagesPromptCost(pruned, 0, profile) - provider.MessagesTokens(pruned, profile)
	target := budget - schemaCost - overhead
	if target < 1 {
		target = 1
	}
	if provider.MessagesTokens(pruned, profile) > target {
		pruned = provider.PruneMessagesKeepTurns(pruned, target, profile)
	}
	return pruned
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
