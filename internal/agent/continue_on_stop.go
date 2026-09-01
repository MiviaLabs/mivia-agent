// Package agent - the SDK ContinueOnStop hook carrying this repo's two
// stop-time continuation policies.
//
// Two host policies re-drive a turn that the SDK loop stopped gracefully:
// retryOnEmptyResponse's subject (a turn with no text and no tool calls) and
// continueUnactedTurn's subject (a turn with real text that announced work
// and never called a tool). Both used to re-run the whole SDK loop as a
// second agentloop.Loop; since mivia-ai-sdk v0.1.3 both ride the loop's own
// Options.ContinueOnStop hook, so a continuation is an ordinary iteration of
// the SAME loop. The loop's MaxIterations therefore bounds the whole turn by
// itself, and the host-side replay step budget that used to paper over the
// fresh-loop counter restart is gone (replay_step_budget.go, deleted).
//
// One property of the old replay does not survive the mechanism and is
// accepted: a replay discarded the failed attempt's history and re-asked
// from the pre-turn messages, while a continuation can only APPEND. The
// triggering (empty) assistant message stays in the run history; the wire
// layers already drop that shape on every provider request
// (toAPIMessages, anthropic_request.go) and DropEmptyAssistantTurns drops
// it before persistence, so nothing poisoned reaches the provider or the
// session. The appended emptyResponseContinuationNotice persists, exactly
// like the unacted continuation notice always has.

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// emptyResponseContinuationNotice is the model-facing nudge the
// empty-response retry appends before the loop's next iteration. The old
// re-run sent the model nothing - it silently replayed the turn from the
// pre-turn history - but a continuation can only append, so the retry must
// say something. RoleUser (RoleSystem is only valid at index 0) and the
// bracket label follow unactedContinuationNotice's rule: the message
// persists in session history, and unlabelled host prose would replay to
// every later turn as the user's own words.
const emptyResponseContinuationNotice = "[mivia: your previous response was empty - no text arrived. " +
	"Reply again with your answer.]"

// newSDKContinueOnStop builds the ContinueOnStop callback for one turn.
// It carries the two continuation policies verbatim from the replay
// functions it replaces:
//
//   - StopEmptyResponse + RequireFinalText + no tool calls in the whole
//     turn + not a refusal -> the bounded empty-response retry
//     (maxEmptyResponseRetries attempts).
//   - StopNoToolCalls + MaxUnactedContinuations > 0 + turnLeftWorkUnacted
//     -> the bounded unacted continuation.
//
// DisableProviderReplay suppresses both: a continuation IS a provider
// replay, the same reason it suppresses the prompt-too-long retry.
//
// turnUserText is the content-match boundary sdkResolvedFinalText and
// sdkTurnMadeToolCalls use to find the current turn inside the decision's
// History - the same boundary the replay functions used on a Result.
//
// The two counters make the hook itself the bound owner: a continuation is
// an iteration of the loop the hook belongs to, so no host budget math is
// needed, only the attempt caps.
func newSDKContinueOnStop(l *Loop, sdkOpts sdkagentloop.Options, opts Options, turn *sdkTurnState, turnUserText string) func(context.Context, sdkagentloop.StopDecision) []sdkshape.Message {
	emptyRetries := 0
	unactedContinuations := 0
	return func(_ context.Context, d sdkagentloop.StopDecision) []sdkshape.Message {
		if opts.DisableProviderReplay {
			return nil
		}
		// The turn's step bound covers continuations: the loop enforces it
		// across its own iterations, and a continuation asked for when the
		// turn has no completed call left to spend would only move the stop
		// reason to StopMaxIterations. This is remainingStepBudget's
		// exhausted branch, carried over verbatim: an exhausted budget
		// returns the turn it already has instead of failing it, and a
		// non-positive bound is the unbounded contract and passes through.
		if sdkOpts.MaxIterations > 0 && turn.currentStep() >= sdkOpts.MaxIterations {
			return nil
		}
		switch d.Stop {
		case sdkagentloop.StopEmptyResponse:
			if !opts.RequireFinalText ||
				emptyRetries >= maxEmptyResponseRetries ||
				!sdkStopLeftEmptyResponse(l, opts, turnUserText, d) {
				return nil
			}
			emptyRetries++
			emit(opts, Event{
				Kind:   EventEmptyResponseRetry,
				Detail: fmt.Sprintf("empty response on attempt %d/%d, retrying...", emptyRetries, maxEmptyResponseRetries+1),
			})
			// The turn restarts from the same messages, so anything already
			// streamed belongs to an attempt that no longer exists. Without
			// this a consumer appends the replay onto the first attempt and
			// shows the answer twice.
			emit(opts, Event{Kind: EventAssistantReset})
			return continueWith(opts, sdkshape.Message{Role: sdkshape.RoleUser, Content: emptyResponseContinuationNotice})
		case sdkagentloop.StopNoToolCalls:
			if opts.MaxUnactedContinuations <= 0 ||
				unactedContinuations >= opts.MaxUnactedContinuations ||
				!turnLeftWorkUnacted(sdkOpts, opts, turnUserText, stopDecisionResult(d), nil) {
				return nil
			}
			unactedContinuations++
			emit(opts, Event{
				Kind:   EventUnactedContinuation,
				Detail: fmt.Sprintf("turn announced work but called no tool, continuing (%d/%d)", unactedContinuations, opts.MaxUnactedContinuations),
			})
			return continueWith(opts, sdkshape.Message{Role: sdkshape.RoleUser, Content: unactedContinuationNotice})
		default:
			return nil
		}
	}
}

// stopDecisionResult projects a StopDecision onto the Result shape the
// predicate helpers read. The decision's Message is the assistant turn that
// ended the run - the same message a graceful Result carries in Final - and
// Stop is the reason being decided, which turnLeftWorkUnacted re-checks.
func stopDecisionResult(d sdkagentloop.StopDecision) sdkagentloop.Result {
	return sdkagentloop.Result{Stop: d.Stop, Final: d.Message, History: d.History}
}

// sdkStopLeftEmptyResponse reports whether one completed graceful stop is
// the genuinely-empty shape the bounded retry targets, ALL of:
// sdkResolvedFinalText empty for the whole turn (a turn with usable earlier
// text, e.g. from a tool-call step, is never retried - retrying would
// discard it for no benefit); no tool call anywhere in the turn's history
// (a retry that could reissue a dispatched tool would duplicate a
// non-idempotent side effect); and l.LastFinishReason not a refusal
// (Anthropic refuses with HTTP 200 and empty content, structurally
// identical to "returned nothing" at the StopEmptyResponse layer - see
// provider.FinishReasonRefusal's doc comment for why a refusal must fail
// instead of retrying against the same safety classifier).
func sdkStopLeftEmptyResponse(l *Loop, opts Options, turnUserText string, d sdkagentloop.StopDecision) bool {
	res := stopDecisionResult(d)
	if strings.TrimSpace(sdkResolvedFinalText(res, turnUserText)) != "" {
		return false
	}
	if sdkTurnMadeToolCalls(d.History, turnUserText) {
		return false
	}
	return l.LastFinishReason != provider.FinishReasonRefusal
}

// continueWith revokes any orphaned optimistic stream content and returns
// the single continuation message. The revoke runs HERE - inside the hook,
// on the continue path only, before the SDK appends the message and starts
// the next iteration - because this is the last host code that runs before
// the next attempt streams into the same FinalWriter, mirroring the exact
// point the replay functions revoked. It must NOT run on a nil return: a
// nil return means the content just streamed IS the turn's final answer
// (finalizeSDKTurn rewrites nothing then), and revoking it would blank the
// answer the user is reading. Revoking is a no-op when nothing streamed or
// the writer does not implement streamRevoker.
func continueWith(opts Options, msg sdkshape.Message) []sdkshape.Message {
	revokeStreamWriter(opts.FinalWriter)
	return []sdkshape.Message{msg}
}
