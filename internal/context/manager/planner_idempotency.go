package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func planSourceRange(input PlanInput) (state.SourceRange, error) {
	var derived state.SourceRange
	if len(input.SourceEvents) > 0 {
		first := input.SourceEvents[0].ID
		last := first
		for index, event := range input.SourceEvents {
			if err := event.Validate(); err != nil {
				return state.SourceRange{}, err
			}
			if event.ID.SessionID != first.SessionID || event.ID.Sequence != first.Sequence+uint64(index) {
				return state.SourceRange{}, invalidPlan("source_events", "events are not contiguous")
			}
			last = event.ID
		}
		derived = state.SourceRange{Start: first, End: last}
	}
	if isZeroSourceRange(input.SourceRange) {
		return derived, nil
	}
	if err := input.SourceRange.Validate(); err != nil {
		return state.SourceRange{}, err
	}
	if !isZeroSourceRange(derived) {
		if input.SourceRange.Start.SessionID != derived.Start.SessionID || input.SourceRange.Start.Sequence > derived.Start.Sequence || input.SourceRange.End.Sequence < derived.End.Sequence {
			return state.SourceRange{}, invalidPlan("source_range", "does not cover source events")
		}
	}
	return input.SourceRange, nil
}

func isZeroSourceRange(r state.SourceRange) bool {
	return r.Start.SessionID == "" && r.End.SessionID == "" && r.Start.Sequence == 0 && r.End.Sequence == 0
}

func planIdempotencyKey(input PlanInput, rng state.SourceRange, target int, retained []provider.Message) (string, error) {
	if input.IdempotencyKey != "" {
		if err := validatePlanKey(input.IdempotencyKey); err != nil {
			return "", err
		}
		return input.IdempotencyKey, nil
	}
	canonical, err := state.MarshalCanonical(struct {
		Algorithm     string
		Range         state.SourceRange
		Budget        int
		Target        int
		OutputReserve int
		FromRevision  state.Revision
		Messages      []plannerMessageFingerprint
	}{compactionAlgorithm, rng, input.Budget, target, input.OutputReserve, input.Revision, plannerMessages(retained)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "compact-" + hex.EncodeToString(digest[:]), nil
}

type plannerMessageFingerprint struct {
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        []plannerToolCallFingerprint
	ToolCallID       string
	Name             string
}

type plannerToolCallFingerprint struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

func plannerMessages(messages []provider.Message) []plannerMessageFingerprint {
	output := make([]plannerMessageFingerprint, len(messages))
	for index, message := range messages {
		output[index] = plannerMessageFingerprint{
			Role:             message.Role,
			Content:          message.Content,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
		}
		for _, call := range message.ToolCalls {
			output[index].ToolCalls = append(output[index].ToolCalls, plannerToolCallFingerprint{
				ID: call.ID, Type: call.Type, Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
	}
	return output
}

func validatePlanKey(key string) error {
	if len(key) > state.MaxIdentifierBytes || strings.TrimSpace(key) != key || key == "" {
		return invalidPlan("idempotency_key", "outside identifier limit")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return invalidPlan("idempotency_key", "contains control characters")
		}
	}
	return nil
}
