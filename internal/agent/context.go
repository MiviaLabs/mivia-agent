package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (l *Loop) prepareStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options) error {
	if opts.PreparationManager == nil {
		l.pruneHistory(opts)
		return promptBudgetErrorWithTools(l.Messages, opts.MaxContextTokens, toolSpecs)
	}
	input := opts.PreparationInput
	input.Messages = l.Messages
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
	preparation, err := opts.PreparationManager.Prepare(ctx, input)
	if err != nil {
		l.PreparationErr = err
		if !l.HasPreparation && interruptedContext(ctx, err) {
			preparation, fallbackErr := opts.PreparationManager.Prepare(context.Background(), input)
			if fallbackErr == nil {
				l.recordPreparation(preparation)
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
