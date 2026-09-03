package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

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
	// The retry re-runs the ENTIRE loop on compacted messages, so any text the
	// first attempt streamed belongs to a run that is being abandoned.
	emit(opts, Event{Kind: EventAssistantReset})
	return run(sdkCompactAfterPromptTooLong(l, opts, preparedMsgs))
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
