package agent

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// promptTooLongCompactNotice is the model-visible notice appended to history
// after a prompt-too-long rejection compacts it. Dropping earlier tool results
// with only an operator EventPrune silently desynchronises the transcript from
// what the model can see: the next step would re-derive or re-read findings
// with no way to know they are gone. RoleUser keeps ValidateToolPairing happy
// (RoleSystem is only valid at index 0) and matches how the loop injects
// step-boundary notes (BeforeStep frames are user-role).
const promptTooLongCompactNotice = "[context compacted: the provider rejected the prompt as too long, so earlier turns and tool results were dropped to fit the model context; re-read any needed file with offset/limit for the remaining parts]"

// retryAfterPromptTooLong compacts history to a small fixed target INDEPENDENT
// of the advertised budget (a 1M-token model profile does not guarantee a 1M
// window) and retries the model call exactly once after the provider rejected
// the prompt as too long. A second rejection is a permanent property of the
// conversation, so the caller propagates it unchanged (fail fast — no second
// retry, no loop). It returns the retry's response/error and the re-estimated
// prompt cost so the success-path calibration events reflect what was sent.
func (l *Loop) retryAfterPromptTooLong(req provider.Request, opts Options, llmCtx context.Context, estimatedTokens int) (*provider.Response, int, error) {
	target := 16 << 10 // 16K tokens: small enough to fit any real limit
	if opts.MaxContextTokens > 0 && opts.MaxContextTokens/4 < target {
		target = opts.MaxContextTokens / 4
	}
	if target < 1 {
		target = 1
	}
	// The compaction notice is appended after pruning and charged against the
	// SAME fixed target: prune to target minus the notice's own cost, then
	// append it, so the retried history (notice included) stays within target.
	notice := provider.Message{Role: provider.RoleUser, Content: promptTooLongCompactNotice}
	pruneTarget := target - provider.MessageTokens(notice)
	if pruneTarget < 1 {
		pruneTarget = 1
	}
	// PruneMessagesKeepTurns always keeps the system prompt and the newest
	// turns, and drops tool exchanges as a unit (announced call + results),
	// so pairing survives. The failed ChatTurn appended nothing, so history
	// holds no orphaned tool calls. The pre-prune history is snapshotted
	// BEFORE the in-place prune: the retry drops messages here, and the
	// summary injected below must reference exactly what this prune omitted.
	// The first prepareStep captured evidence for the rejected, never-sent
	// history, so without this snapshot the retried request's summary would
	// reference the wrong omitted set (R0-1). PruneMessagesKeepTurns never
	// mutates its input, so the snapshot stays a valid copy of the rejected
	// history.
	prePrune := l.Messages
	l.Messages = provider.PruneMessagesKeepTurns(l.Messages, pruneTarget)
	// The compaction must be visible to the model, not just the operator:
	// earlier tool results were dropped, and the next step has no way to know
	// unless history says so. RoleUser keeps ValidateToolPairing happy
	// (RoleSystem is only valid at index 0), matching how the loop injects
	// step-boundary notes (BeforeStep frames are user-role).
	l.Messages = append(l.Messages, notice)
	emit(opts, Event{
		Kind:   EventPrune,
		Detail: fmt.Sprintf("provider rejected prompt (prompt too long); compacted to %d tokens and retrying once", target),
	})
	req.Messages = l.Messages
	// Phase 2 summary injection on the retry: the pruned history still belongs
	// to a compacted turn, so carry the summary into the retried request when
	// the budget re-check passes. On any failure the retry stays structural.
	if opts.SummaryConfig.Summarizer != nil && l.HasPreparation && l.LastPreparation.Compacted {
		// Re-derive the omitted-evidence diff for the history the retry
		// actually sends BEFORE the summary is built: summarizeTurn reads the
		// run's TurnState, which otherwise still describes the rejected
		// first-attempt history (R0-1). The pre-prune snapshot also becomes
		// the excerpt source, so the summary quotes what THIS prune dropped.
		l.preCompactSource = prePrune
		l.refreshOmittedEvidenceAfterRetry(prePrune)
		req.Messages = l.injectSummary(llmCtx, opts)
	}
	// The compacted retry prompt is genuinely new work (charge it against the
	// prompt bound), but its output was already reserved by the rejected first
	// attempt: reserveProvider would charge one logical completion's output
	// allowance twice, hard-failing a finite MaxOutputTokens budget even though
	// the rejected call produced no output. reservePromptOnly charges prompt
	// only; the cumulative output bound still holds.
	estimatedTokens, _ = provider.EstimatePromptCost(req.Messages, req.Tools)
	if err := l.workLimits.reservePromptOnly(estimatedTokens); err != nil {
		return nil, estimatedTokens, err
	}
	resp, err := l.Completer.ChatTurn(llmCtx, req)
	return resp, estimatedTokens, err
}
