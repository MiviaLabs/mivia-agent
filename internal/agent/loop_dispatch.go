package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// errSteerInterrupt marks a turn a soft interrupt (plan 54) canceled:
// the interrupted step's partial reply is returned alongside this
// sentinel instead of a raw context.Canceled, so a caller can
// distinguish "the model was asked to stop and complied" from a hard
// cancellation. Test with errors.Is.
var errSteerInterrupt = errors.New("agent: steer interrupt")

// runOnce is the driver behind (*Loop).Run. It always runs the
// SDK-backed loop through RunAgentLoopOnce (completer wrapper,
// registry converter, steer bridge, and the fail-closed Options
// checks all live there). The legacy pre-SDK engine that once ran
// here for callers of options the SDK path could not carry is gone:
// every field the legacy loop honored - including Surface rotation
// and every WorkLimits reservation - is carried on this path now
// (docs/development/sdk-backend-field-mapping.md).
func (l *Loop) runOnce(ctx context.Context, userText string, opts Options) (string, error) {
	return l.runOnceSDK(ctx, userText, opts)
}

// runOnceSDK drives one SDK-backed turn and translates the SDK Result
// onto the legacy (string, error) contract. The message history is the
// loop's carried history plus the user's text, matching what the
// legacy path feeds its first provider call. A steered stop maps to
// the loop's existing errSteerInterrupt sentinel so callers handle
// both backends with one error identity; every other graceful stop
// returns the final assistant content.
func (l *Loop) runOnceSDK(ctx context.Context, userText string, opts Options) (string, error) {
	// Each Run owns its finish-reason report, mirroring runOnceLegacy's
	// reset: a previous run's reason must never leak into the next
	// caller's read. The SDK path leaves the field empty because the
	// SDK's Message shape carries no finish reason.
	l.LastFinishReason = ""
	// Run-start resets, mirroring runOnceLegacy: stale preparation from
	// a previous turn is discarded, the turn compaction counters reset,
	// and the turn gets a fresh TurnState. The per-iteration Trim
	// closure then re-records preparation on every Completer call.
	l.discardPreparation(opts)
	l.resetTurnCompaction()
	l.TurnState = contextmgr.NewTurnState()
	// Pre-append the user message to the carried history BEFORE the run,
	// mirroring runOnceLegacy's append (loop.go): a turn that fails
	// before any SDK iteration completes must still keep the user
	// message in l.Messages, because the SDK's hard-fail Result is empty
	// when iterations == 0 and a write-back-only path would lose the
	// whole turn. The slice handed to RunAgentLoopOnce is a copy of the
	// post-append history, so the user message is never added twice.
	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleUser,
		Content:   userText,
		CreatedAt: time.Now(),
	})
	msgs := make([]provider.Message, len(l.Messages))
	copy(msgs, l.Messages)
	preLen := len(l.Messages)
	res, err := RunAgentLoopOnce(ctx, l, opts, msgs)
	l.writeBackSDKHistory(res, preLen)
	if err != nil {
		// A canceled or timed-out run keeps the partial reply the turn
		// already produced, mirroring the legacy interrupted-step
		// contract (Loop.Run returns lastText on an interrupted step;
		// the subagent pool maps it to status canceled/timed_out and
		// keeps the output). Every other error returns empty so a raw
		// provider body cannot leak (the pinned guarantee in
		// TestMultiStepHandlerFailureOmitsRawProviderBodyAndRefs).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			startIdx := sdkCurrentTurnStart(res.History, userText)
			for i := len(res.History) - 1; i >= startIdx; i-- {
				if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
					return m.Content, err
				}
			}
		}
		return "", err
	}
	if res.Stop == sdkagentloop.StopSteered {
		return sdkSteeredStopPartial(res.History, userText)
	}
	if res.Stop == sdkagentloop.StopMaxIterations {
		// Legacy parity: exceeding the step cap is a hard error naming
		// the cap, not a graceful partial answer.
		return "", fmt.Errorf("agent exceeded max_steps (%d)", effectiveSDKMaxIterations(opts))
	}
	if strings.TrimSpace(res.Final.Content) == "" {
		startIdx := sdkCurrentTurnStart(res.History, userText)
		for i := len(res.History) - 1; i >= startIdx; i-- {
			if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				return m.Content, nil
			}
		}
	}
	return res.Final.Content, nil
}

// writeBackSDKHistory applies runOnceSDK's post-run history write-back.
// Messages RunAgentLoopOnce appended past the pre-append (the streamed
// partial recorded on a cancel) must survive the write-back, so they
// are captured first as extras. preLen can no longer be trusted as an
// exact boundary into l.Messages: the no-PreparationManager preflight
// (sdkPromptBudgetPreflight) may have PRUNED l.Messages below preLen
// before any iteration ran, replacing it wholesale rather than only
// appending to it - clamped here so a pruned run never slices out of
// range. A non-empty res.History then replaces the carried history
// wholesale (the success path's rule); an empty res.History keeps the
// pre-appended l.Messages so the turn is never lost. Either way, extras
// lands after the replaced history, preserving emission order.
func (l *Loop) writeBackSDKHistory(res sdkagentloop.Result, preLen int) {
	preLen = min(preLen, len(l.Messages))
	extras := append([]provider.Message(nil), l.Messages[preLen:]...)
	if len(res.History) > 0 {
		fresh := restoreSDKHistoryTimestamps(sdkMessagesToCLI(res.History), l.Messages[:preLen])
		l.Messages = stripInjectedSummaryFrames(fresh)
	}
	l.Messages = append(l.Messages, extras...)
}

// stripInjectedSummaryFrames removes every context-summary frame from a
// converted SDK history before it becomes the loop's carried l.Messages.
//
// injectSummary's own contract is that the summary lives only in the
// ephemeral per-request slice returned from Trim - "It never mutates
// l.Messages... which must stay structural so planning, idempotency,
// BaseDigest, and checkpoint bytes are unaffected." The SDK's own contract
// disagrees: agentloop/run.go treats Trim's return as the run's real carried
// history (*history = trimmed), so every summary-injected Trim call leaks its
// frame into res.History. Left unfiltered, writeBackSDKHistory would copy
// that leaked frame straight into l.Messages, making it a second, permanent
// copy on top of the one commitContextTurn separately appends (with its Name
// stripped) for the durable checkpoint - the exact duplication tracked in
// docs/development/sdk-backend-field-mapping.md §4. Filtering here restores
// the documented invariant at the one seam that actually violates it, without
// reaching into the external SDK package.
//
// Run AFTER restoreSDKHistoryTimestamps, not before: that function aligns
// timestamps positionally against the pre-turn prefix, and the leaked frame
// can sit anywhere in the interior of a multi-step history, not only at the
// tail - removing it first would shift every later index out of alignment.
func stripInjectedSummaryFrames(messages []provider.Message) []provider.Message {
	out := messages[:0]
	for _, m := range messages {
		if m.Name == SummaryMessageName {
			continue
		}
		out = append(out, m)
	}
	return out
}

// sdkCurrentTurnStart returns the index in `history` of the first
// in-scope message of the current turn. Locate by matching the user
// message Content from history backward; a stale index computed
// against the pre-prepare msgs slice would land past the start of
// `history` when prepareSDKHistory compacts the pre-turn prefix.
func sdkCurrentTurnStart(history []sdkshape.Message, userText string) int {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role == sdkshape.RoleUser && m.Content == userText {
			return i + 1
		}
	}
	return 0
}

// sdkSteeredStopPartial returns the partial reply a steered-stop run
// carries. The SDK cancels the in-flight Completer.Chat wholesale on
// Trigger, so the bytes emitted inside the canceled call are lost;
// what survives in `history` are the assistant messages appended
// before the cancel. The function locates the current turn's start
// marker (the most recent user-role message whose Content matches
// `userText`) and walks history backward from there, returning the
// most recent non-empty assistant content along with
// `errSteerInterrupt`, mirroring the legacy `lastText` contract at
// loop.go:143-179.
//
// Locating the boundary by Content match (not by an index precomputed
// against the pre-prepare msgs) is load-bearing: when a
// PreparationManager is wired, prepareSDKHistory may shrink the
// pre-turn prefix, so an index computed from the original msgs slice
// would land past the start of `history` and the walk would never
// execute. Content match is robust against that prefix compaction.
//
// A steered-stop run with no surviving in-scope assistant content
// returns ("", errSteerInterrupt) so callers that read text=empty
// recognize the truly-empty case.
func sdkSteeredStopPartial(history []sdkshape.Message, userText string) (string, error) {
	startIdx := 0
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role == sdkshape.RoleUser && m.Content == userText {
			startIdx = i + 1
			break
		}
	}
	for i := len(history) - 1; i >= startIdx; i-- {
		m := history[i]
		if m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content, errSteerInterrupt
		}
	}
	return "", errSteerInterrupt
}

// restoreSDKHistoryTimestamps copies CreatedAt from the pre-turn slice
// onto the converted history's matching prefix (the SDK Message shape
// carries no timestamp) and stamps the turn's new messages with the
// current time, matching the legacy path's time.Now() appends.
func restoreSDKHistoryTimestamps(fresh, old []provider.Message) []provider.Message {
	now := time.Now()
	for i := range fresh {
		if i < len(old) {
			fresh[i].CreatedAt = old[i].CreatedAt
		} else if fresh[i].CreatedAt.IsZero() {
			fresh[i].CreatedAt = now
		}
	}
	return fresh
}

// effectiveSDKMaxIterations mirrors buildAgentLoopOptions' iteration
// clamp so an error message can name the same cap the SDK enforced.
// When MaxSteps is unset (0), the SDK treats it as uncapped; the
// error path is unreachable in practice but is preserved so the
// message reports "no cap" honestly if it ever fires.
func effectiveSDKMaxIterations(opts Options) int {
	limit := opts.MaxSteps
	if wl := opts.WorkLimits.MaxTurns; wl > 0 && (opts.MaxSteps <= 0 || wl < opts.MaxSteps) {
		limit = wl
	}
	return limit
}
