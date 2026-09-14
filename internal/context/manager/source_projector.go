package manager

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ProjectSource is the allowlisted source boundary for a completed turn. It
// records message metadata and, when the workspace explicitly configures a
// redaction classifier, bounded sanitized payloads. System prompts, tool-call
// arguments, and hidden provider fields never cross this boundary.
func ProjectSource(ctx context.Context, principal state.Principal, messages []provider.Message, firstSequence uint64, policy state.RedactionPolicy) ([]state.SourceEvent, []state.PayloadRecord, error) {
	if err := principal.Validate(); err != nil {
		return nil, nil, err
	}
	if !principal.IsBound() {
		return nil, nil, fmt.Errorf("%w: owner capability is not bound", state.ErrPrincipalMismatch)
	}
	if firstSequence == 0 {
		firstSequence = 1
	}
	if state.Exceeds(len(messages), state.CurrentLimits().CommitEvents) {
		return nil, nil, fmt.Errorf("%w: turn contains too many source messages", state.ErrInvalidDTO)
	}
	events := make([]state.SourceEvent, 0, len(messages))
	payloads := make([]state.PayloadRecord, 0, len(messages))
	for _, message := range messages {
		if err := sourceMessageRole(message.Role); err != nil {
			return nil, nil, err
		}
		if message.Role == provider.RoleSystem {
			continue
		}
		id, err := state.NewSourceID(principal.SessionID, firstSequence+uint64(len(events)))
		if err != nil {
			return nil, nil, err
		}
		event := state.SourceEvent{
			ID: id, Kind: sourceKind(message), Role: message.Role,
			ToolCallID: message.ToolCallID, Provenance: "host-turn",
			RedactionStatus: "metadata",
		}
		if message.Content != "" {
			payload, err := state.SanitizeSourcePayload(ctx, principal, []byte(message.Content), policy)
			if err != nil {
				return nil, nil, err
			}
			event.PayloadRef = payload.Ref.Ref
			event.Size = payload.Ref.Size
			if payload.Dereferenceable {
				event.RedactionStatus = "sanitized"
			}
			payloads = append(payloads, state.PayloadRecord{
				Ref: payload.Ref, Retention: payload.Retention, Data: append([]byte(nil), payload.Bytes...),
			})
		}
		if err := event.Validate(); err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, nil, fmt.Errorf("%w: turn has no persistable source messages", state.ErrInvalidDTO)
	}
	return events, payloads, nil
}

func sourceMessageRole(role string) error {
	switch role {
	case provider.RoleSystem, provider.RoleUser, provider.RoleAssistant, provider.RoleTool:
		return nil
	default:
		return fmt.Errorf("%w: source message role %q is not allowlisted", state.ErrInvalidDTO, role)
	}
}

func sourceKind(message provider.Message) string {
	switch {
	case message.Role == provider.RoleTool:
		return "tool_result"
	case len(message.ToolCalls) > 0:
		return "tool_call"
	default:
		return "message"
	}
}
