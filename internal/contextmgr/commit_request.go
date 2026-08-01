package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

const (
	OutcomeComplete    = "complete"
	OutcomeCancelled   = "cancelled"
	OutcomeTruncated   = "truncated"
	OutcomeUpstreamErr = "upstream-error"
)

// BuildCommitRequest is the only conversion from provider-facing turn state
// into the durable context contract. It validates all captured fences before
// constructing bytes that storage may publish.
func BuildCommitRequest(_ context.Context, preparation Preparation, result TurnResult, principal contextstate.Principal, expected contextstate.Revision, binding contextstate.BindingRevision) (contextstate.CommitRequest, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.CommitRequest{}, err
	}
	if err := preparation.ValidateToken(expected, binding); err != nil {
		return contextstate.CommitRequest{}, err
	}
	if preparation.Token.Principal != principal {
		return contextstate.CommitRequest{}, fmt.Errorf("%w: preparation principal changed", contextstate.ErrPrincipalMismatch)
	}
	if preparation.Token.IdempotencyKey == "" {
		return contextstate.CommitRequest{}, fmt.Errorf("%w: missing operation key", contextstate.ErrInvalidDTO)
	}
	if result.TurnID == 0 || len(result.Active) == 0 {
		return contextstate.CommitRequest{}, fmt.Errorf("%w: missing post-turn state", contextstate.ErrInvalidDTO)
	}
	if !validOutcome(result.Outcome) {
		return contextstate.CommitRequest{}, fmt.Errorf("%w: invalid turn outcome", contextstate.ErrInvalidDTO)
	}
	ordered := orderedTurnMessages(result)
	if err := validateMessageShape(ordered); err != nil {
		return contextstate.CommitRequest{}, err
	}
	activeContext, err := contextstate.MarshalCanonical(result.Active)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	baseDigest := result.BaseDigest
	if baseDigest == "" {
		baseBytes, marshalErr := contextstate.MarshalCanonical(preparation.Messages)
		if marshalErr != nil {
			return contextstate.CommitRequest{}, marshalErr
		}
		digest := sha256.Sum256(baseBytes)
		baseDigest = hex.EncodeToString(digest[:])
	}
	// TurnResult owns the newly appended source events. Candidate source events
	// describe the covered range and are only used for a foundation-only
	// preparation that has no completed turn envelope; never re-append old
	// history merely because a checkpoint summarizes it.
	events := append([]contextstate.SourceEvent(nil), result.SourceEvents...)
	if len(events) == 0 {
		events = append(events, preparation.Candidate.SourceEvents...)
	}
	newSource := expected.Source + uint64(len(events))
	rng, err := commitRange(preparation.Candidate.SourceRange, principal.SessionID, expected.Source, newSource)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	checkpointID, err := contextstate.NewCheckpointID(principal.SessionID, rng, preparation.Candidate.CandidateAlgorithm(), 1, binding.Model, preparation.Token.IdempotencyKey)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	checkpoint := contextstate.CheckpointRecord{
		ID: checkpointID, Revision: contextstate.Revision{Session: expected.Session + 1, Durable: expected.Durable + 1, Source: newSource},
		Binding: binding, SourceRange: rng, ActiveContext: activeContext,
		SummaryMetadata: append([]byte(nil), preparation.Candidate.SummaryMetadata...), TurnID: result.TurnID, Complete: true,
	}
	request := contextstate.CommitRequest{
		OperationID: preparation.Token.IdempotencyKey,
		Principal:   principal, SessionID: principal.SessionID, Expected: expected, ExpectedBinding: binding,
		NewSourceEvents: events, Payloads: append([]contextstate.PayloadRecord(nil), preparation.Candidate.Payloads...),
		Checkpoint: checkpoint, ActiveContext: activeContext, NewSession: expected.Session + 1,
		NewDurable: expected.Durable + 1, NewSourceSequence: newSource, NewBinding: binding,
		TurnID: result.TurnID, BaseDigest: baseDigest,
	}
	if err := request.Validate(); err != nil {
		return contextstate.CommitRequest{}, err
	}
	return request, nil
}

func orderedTurnMessages(result TurnResult) []provider.Message {
	if len(result.Ordered) > 0 {
		return cloneMessages(result.Ordered)
	}
	out := make([]provider.Message, 0, len(result.User)+len(result.Assistant)+len(result.Tool))
	out = append(out, result.User...)
	out = append(out, result.Assistant...)
	out = append(out, result.Tool...)
	return out
}

func validOutcome(outcome string) bool {
	switch outcome {
	case OutcomeComplete, OutcomeCancelled, OutcomeTruncated, OutcomeUpstreamErr:
		return true
	default:
		return false
	}
}

func commitRange(candidate contextstate.SourceRange, session string, expectedSource, newSource uint64) (contextstate.SourceRange, error) {
	if candidate.Start.SessionID != session || candidate.End.SessionID != session {
		return contextstate.SourceRange{}, fmt.Errorf("%w: candidate range belongs to another session", contextstate.ErrPrincipalMismatch)
	}
	start := candidate.Start.Sequence
	if start == 0 {
		start = 1
	}
	if start > expectedSource+1 || newSource < start {
		return contextstate.SourceRange{}, fmt.Errorf("%w: candidate range is not contiguous", contextstate.ErrInvalidDTO)
	}
	result := contextstate.SourceRange{Start: contextstate.SourceID{SessionID: session, Sequence: start}, End: contextstate.SourceID{SessionID: session, Sequence: newSource}}
	if err := result.Validate(); err != nil {
		return contextstate.SourceRange{}, err
	}
	return result, nil
}

func (c CheckpointCandidate) CandidateAlgorithm() string {
	return "context-compact-v1"
}

func validateMessageShape(messages []provider.Message) error {
	seenCalls := map[string]struct{}{}
	seenResults := map[string]struct{}{}
	for _, message := range messages {
		switch message.Role {
		case provider.RoleSystem, provider.RoleUser, provider.RoleAssistant, provider.RoleTool:
		default:
			return fmt.Errorf("%w: unsupported message role", contextstate.ErrInvalidDTO)
		}
		if message.Role == provider.RoleAssistant {
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					return fmt.Errorf("%w: tool call has no ID", contextstate.ErrInvalidDTO)
				}
				if _, exists := seenCalls[call.ID]; exists {
					return fmt.Errorf("%w: duplicate tool call ID", contextstate.ErrInvalidDTO)
				}
				seenCalls[call.ID] = struct{}{}
			}
		}
		if message.Role == provider.RoleTool {
			if message.ToolCallID == "" {
				return fmt.Errorf("%w: tool result has no ID", contextstate.ErrInvalidDTO)
			}
			if _, exists := seenCalls[message.ToolCallID]; !exists {
				return fmt.Errorf("%w: orphan tool result", contextstate.ErrInvalidDTO)
			}
			if _, exists := seenResults[message.ToolCallID]; exists {
				return fmt.Errorf("%w: duplicate tool result", contextstate.ErrInvalidDTO)
			}
			seenResults[message.ToolCallID] = struct{}{}
		}
	}
	for id := range seenCalls {
		if _, ok := seenResults[id]; !ok {
			return fmt.Errorf("%w: unterminated tool call %q", contextstate.ErrInvalidDTO, id)
		}
	}
	return nil
}
