package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (l *Loop) prepareStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options) error {
	// Parent-message inject (plan 53.03): before prune so injected frames are
	// part of a complete turn for PruneMessagesKeepTurns.
	if opts.BeforeStep != nil {
		if injected := opts.BeforeStep(); len(injected) > 0 {
			l.Messages = append(l.Messages, injected...)
		}
	}
	if opts.PreparationManager == nil {
		l.pruneHistory(opts, toolSpecs)
		return promptBudgetErrorWithTools(l.Messages, opts.MaxContextTokens, toolSpecs)
	}
	input := l.buildPrepareInput(toolSpecs, opts)
	preparation, err := opts.PreparationManager.Prepare(ctx, input)
	if err != nil {
		l.PreparationErr = err
		if !l.HasPreparation && opts.WorkLimits.DeadlineAt.IsZero() && interruptedContext(ctx, err) {
			preparation, fallbackErr := opts.PreparationManager.Prepare(context.Background(), input)
			if fallbackErr == nil {
				l.recordPreparation(preparation)
				l.captureOmittedEvidence(input, preparation)
				l.Messages = clonePreparedMessages(preparation.Messages)
				l.PreparationErr = nil
			} else {
				l.PreparationErr = fallbackErr
			}
		}
		return err
	}
	l.discardPreparation(opts)
	l.recordPreparation(preparation)
	l.PreparationErr = nil
	l.captureOmittedEvidence(input, preparation)
	l.Messages = clonePreparedMessages(l.LastPreparation.Messages)
	return nil
}

// captureOmittedEvidence folds the content-free diff of the pre-compaction
// history against the retained preparation into the run's TurnState BEFORE
// l.Messages is overwritten. Omitted evidence is bounded by the tracker; a
// rejected item (list full, envelope-invalid) is dropped, never an error.
func (l *Loop) captureOmittedEvidence(input contextmgr.PrepareInput, preparation contextmgr.Preparation) {
	if l.TurnState == nil || !preparation.Compacted {
		return
	}
	for _, item := range contextmgr.OmittedEvidence(input.Messages, preparation.Messages) {
		_ = l.TurnState.AddEvidence(item)
	}
}

// buildPrepareInput assembles the manager input shared by prepareStep and the
// prompt-too-long retry re-preparation (refreshPreparationAfterRetry), so both
// call sites price the SAME history, budget, tools, reserve, objective, and
// calibration. It mirrors the caller-supplied PrepareInput, overlaying the
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
	if input.CurrentObjective == "" {
		input.CurrentObjective = latestUserObjective(l.Messages)
	}
	if l.Calibration.Samples > 0 {
		input.CalibrationRatio = l.Calibration.Ratio
	}
	return input
}

// refreshPreparationAfterRetry re-synchronizes the recorded preparation with
// the history a prompt-too-long retry actually sent. retryAfterPromptTooLong
// pruned l.Messages in place and appended the compaction notice, leaving
// LastPreparation pointing at the rejected (never-sent) history; committing
// that stale preparation would fingerprint a BaseDigest over bytes the
// checkpoint does not hold. Discard it and re-Prepare on the pruned history so
// commit and checkpoint agree. A re-Prepare failure fails the turn honestly:
// a checkpoint built from a preparation that could not be produced is worse
// than none. With no manager configured, discarding is enough - nothing stale
// can be committed.
func (l *Loop) refreshPreparationAfterRetry(ctx context.Context, req provider.Request, opts Options) error {
	l.discardPreparation(opts)
	if opts.PreparationManager == nil {
		return nil
	}
	input := l.buildPrepareInput(req.Tools, opts)
	preparation, err := opts.PreparationManager.Prepare(ctx, input)
	if err != nil {
		l.PreparationErr = err
		return err
	}
	l.recordPreparation(preparation)
	l.PreparationErr = nil
	l.Messages = clonePreparedMessages(l.LastPreparation.Messages)
	return nil
}

// recordPreparation stores the step preparation and overlays turn-level
// compaction accounting so a later non-compacting step cannot erase an
// earlier elision from the sealed event and LastPreparation counters.
func (l *Loop) recordPreparation(preparation contextmgr.Preparation) {
	if preparation.Compacted {
		if !l.turnCompacted {
			l.turnCompacted = true
			l.turnBeforeTokens = preparation.BeforeTokens
		}
		l.turnAfterTokens = preparation.AfterTokens
		l.turnElidedMessages += preparation.ElidedMessages
		l.turnElidedBytes += preparation.ElidedBytes
	}
	if l.turnCompacted {
		preparation.Compacted = true
		preparation.BeforeTokens = l.turnBeforeTokens
		preparation.AfterTokens = l.turnAfterTokens
		preparation.ElidedMessages = l.turnElidedMessages
		preparation.ElidedBytes = l.turnElidedBytes
	}
	l.LastPreparation = preparation
	l.HasPreparation = true
}

func (l *Loop) resetTurnCompaction() {
	l.turnCompacted = false
	l.turnBeforeTokens = 0
	l.turnAfterTokens = 0
	l.turnElidedMessages = 0
	l.turnElidedBytes = 0
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

func promptBudgetError(messages []provider.Message, budget int) error {
	return promptBudgetErrorWithTools(messages, budget, nil)
}

func promptBudgetErrorWithTools(messages []provider.Message, budget int, tools []provider.ToolSpec) error {
	if budget <= 0 {
		return nil
	}
	tokens, err := provider.EstimatePromptCost(messages, tools)
	if err != nil {
		return fmt.Errorf("%w: estimate request cost: %v", ErrPromptBudgetExceeded, err)
	}
	if tokens <= budget {
		return nil
	}
	return fmt.Errorf("%w (%d > %d tokens)", ErrPromptBudgetExceeded, tokens, budget)
}
