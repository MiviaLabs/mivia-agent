package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// StructuralPreparationManager adapts the pure planner to the preparation
// capability. It owns no storage and never publishes a checkpoint.
type StructuralPreparationManager struct {
	Tools         []map[string]any
	OutputReserve int
	RecentTail    int
}

func (m StructuralPreparationManager) Prepare(ctx context.Context, input PrepareInput) (Preparation, error) {
	if err := contextDone(ctx); err != nil {
		return Preparation{}, err
	}
	if err := input.Principal.Validate(); err != nil {
		return Preparation{}, err
	}
	rangeValue := input.SourceRange
	if isZeroSourceRange(rangeValue) {
		rangeValue = contextstate.SourceRange{
			Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
			End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		}
	}
	toolSpecs := input.Tools
	if len(m.Tools) > 0 {
		toolSpecs = cloneToolSpecs(m.Tools)
	}
	plan, err := Plan(PlanInput{
		Messages: input.Messages, Budget: input.Budget, Tools: toolSpecs,
		OutputReserve: input.OutputReserve, Force: input.Force, CurrentObjective: input.CurrentObjective,
		SourceRange: rangeValue, RecentTail: input.RecentTail, CalibrationRatio: input.CalibrationRatio,
	})
	if err != nil {
		return Preparation{}, err
	}
	if !plan.Compacted {
		active, marshalErr := contextstate.MarshalCanonical(plan.Messages)
		if marshalErr != nil {
			return Preparation{}, marshalErr
		}
		plan.Candidate = CheckpointCandidate{ActiveContext: active, SourceRange: rangeValue}
	}
	key := plan.IdempotencyKey
	if key == "" {
		key, err = structuralPreparationKey(plan.Messages, rangeValue, input.Revision)
		if err != nil {
			return Preparation{}, err
		}
	}
	preparation, err := CapturePreparation(input, plan.Candidate, plan.Messages, plan.Compacted, key)
	if err != nil {
		return Preparation{}, err
	}
	preparation.BeforeTokens = plan.BeforeTokens
	preparation.AfterTokens = plan.AfterTokens
	preparation.TriggerTokens = plan.TriggerTokens
	preparation.TargetTokens = plan.TargetTokens
	preparation.ElidedMessages = plan.ElidedMessages
	preparation.ElidedBytes = plan.ElidedBytes
	return preparation, nil
}

func (StructuralPreparationManager) Discard(Preparation) {}

func contextDone(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneToolSpecs(tools []map[string]any) []map[string]any {
	return append([]map[string]any(nil), tools...)
}

func structuralPreparationKey(messages []provider.Message, sourceRange contextstate.SourceRange, revision contextstate.Revision) (string, error) {
	data, err := contextstate.MarshalCanonical(struct {
		Messages []provider.Message
		Range    contextstate.SourceRange
		Revision contextstate.Revision
	}{messages, sourceRange, revision})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	key := "prepare-" + hex.EncodeToString(digest[:])
	if len(key) > contextstate.MaxIdentifierBytes {
		return "", fmt.Errorf("%w: preparation key is too large", contextstate.ErrInvalidDTO)
	}
	return key, nil
}
