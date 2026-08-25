package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// captureOmittedEvidence folds the content-free diff of the pre-compaction
// history against the retained preparation into the run's TurnState BEFORE
// l.Messages is overwritten, and stashes the pre-compaction history so the
// summary request can quote the dropped messages' real content. Omitted
// evidence is bounded by the tracker; a rejected item (list full,
// envelope-invalid) is dropped, never an error.
func (l *Loop) captureOmittedEvidence(input contextmgr.PrepareInput, preparation contextmgr.Preparation) {
	l.preCompactSource = nil
	if !preparation.Compacted {
		return
	}
	l.preCompactSource = clonePreparedMessages(input.Messages)
	if l.TurnState == nil {
		return
	}
	for _, item := range contextmgr.OmittedEvidence(input.Messages, preparation.Messages) {
		_ = l.TurnState.AddEvidence(item)
	}
}

// buildPrepareInput assembles the manager input for step preparation so all
// preparation calls price the SAME history, budget, tools, reserve, objective,
// and calibration. It mirrors the caller-supplied PrepareInput, overlaying the
// loop's live state.
func (l *Loop) buildPrepareInput(toolSpecs []provider.ToolSpec, opts Options) contextmgr.PrepareInput {
	input := opts.PreparationInput
	input.Messages = l.Messages
	input.Spool = opts.RemainderSpool
	if opts.MaxContextTokens > 0 {
		input.Budget = opts.MaxContextTokens
	}
	input.Tools = toolSpecs
	input.OutputReserve = outputReserve(opts.MaxTokens)
	input.ContextAccounting = l.contextAccounting()
	if input.CurrentObjective == "" {
		input.CurrentObjective = latestUserObjective(l.Messages)
	}
	if l.Calibration.Samples > 0 {
		input.CalibrationRatio = l.Calibration.Ratio
	}
	return input
}

// recordPreparation stores the step preparation and overlays turn-level
// compaction accounting so a later non-compacting step cannot erase an
// earlier elision from the sealed event and LastPreparation counters.
func (l *Loop) recordPreparation(preparation contextmgr.Preparation) {
	if preparation.Compacted {
		// The raw (pre-overlay) Compacted flag marks a REAL compaction event.
		// Its token identity keys the summary memo: a later step whose
		// preparation only carries the overlay keeps the key, so the memoized
		// summary is reused; a new compaction changes it.
		l.turnCompactionKey = compactionIdentity(preparation.Token)
		if !l.turnCompacted {
			l.turnCompacted = true
			l.turnBeforeTokens = preparation.BeforeTokens
		}
		l.turnAfterTokens = preparation.AfterTokens
		l.turnElidedMessages += preparation.ElidedMessages
		l.turnElidedBytes += preparation.ElidedBytes
		l.turnElidedReasoningMessages += preparation.ElidedReasoningMessages
		l.turnElidedReasoningBytes += preparation.ElidedReasoningBytes
	}
	if l.turnCompacted {
		preparation.Compacted = true
		preparation.BeforeTokens = l.turnBeforeTokens
		preparation.AfterTokens = l.turnAfterTokens
		preparation.ElidedMessages = l.turnElidedMessages
		preparation.ElidedBytes = l.turnElidedBytes
		preparation.ElidedReasoningMessages = l.turnElidedReasoningMessages
		preparation.ElidedReasoningBytes = l.turnElidedReasoningBytes
	}
	l.LastPreparation = preparation
	l.HasPreparation = true
}

func (l *Loop) resetTurnCompaction() {
	l.turnCompacted = false
	l.turnCompactionEmitted = false
	l.lastEmittedCompactionKey = ""
	l.turnBeforeTokens = 0
	l.turnAfterTokens = 0
	l.turnElidedMessages = 0
	l.turnElidedBytes = 0
	l.turnElidedReasoningMessages = 0
	l.turnElidedReasoningBytes = 0
	l.turnCompactionKey = ""
	l.invalidateSummaryMemo()
	l.summaryMemoKey = ""
}

// compactionIdentity derives the memo key for one compaction event from its
// commit token. The planner's idempotency key already fingerprints the
// operation; the source range is the fallback for callers that construct a
// preparation without one.
func compactionIdentity(token contextmgr.CommitToken) string {
	if token.IdempotencyKey != "" {
		return token.IdempotencyKey
	}
	r := token.Range
	return fmt.Sprintf("range:%s/%d-%s/%d", r.Start.SessionID, r.Start.Sequence, r.End.SessionID, r.End.Sequence)
}

func interruptedContext(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)))
}

func (l *Loop) discardPreparation(opts Options) {
	if !l.HasPreparation {
		return
	}
	if opts.PreparationManager != nil {
		opts.PreparationManager.Discard(l.LastPreparation)
	}
	l.LastPreparation = contextmgr.Preparation{}
	l.HasPreparation = false
	// Turn accumulators are reset only at Run start (resetTurnCompaction).
	// Discard between steps must keep mid-turn elision totals.
}

func outputReserve(maxTokens *int) int {
	if maxTokens == nil || *maxTokens < 0 {
		return 0
	}
	return *maxTokens
}

func latestUserObjective(messages []provider.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return messages[index].Content
		}
	}
	return ""
}

func clonePreparedMessages(messages []provider.Message) []provider.Message {
	output := make([]provider.Message, len(messages))
	copy(output, messages)
	for index := range output {
		output[index].ToolCalls = append([]provider.ToolCall(nil), messages[index].ToolCalls...)
	}
	return output
}

func promptBudgetError(messages []provider.Message, budget int, profile provider.ContextAccountingProfile) error {
	return promptBudgetErrorWithTools(messages, budget, nil, profile)
}

func promptBudgetErrorWithTools(messages []provider.Message, budget int, tools []provider.ToolSpec, profile provider.ContextAccountingProfile) error {
	if budget <= 0 {
		return nil
	}
	tokens, err := provider.EstimatePromptCost(messages, tools, profile)
	if err != nil {
		return fmt.Errorf("%w: estimate request cost: %v", ErrPromptBudgetExceeded, err)
	}
	if tokens <= budget {
		return nil
	}
	return fmt.Errorf("%w (%d > %d tokens)", ErrPromptBudgetExceeded, tokens, budget)
}
