package contextmgr

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ProjectSource is the allowlisted source boundary for a completed turn. It
// records message metadata and, when the workspace explicitly configures a
// redaction classifier, bounded sanitized payloads. System prompts, tool-call
// arguments, and hidden provider fields never cross this boundary.
func ProjectSource(ctx context.Context, principal contextstate.Principal, messages []provider.Message, firstSequence uint64, policy contextstate.RedactionPolicy) ([]contextstate.SourceEvent, []contextstate.PayloadRecord, error) {
	if err := principal.Validate(); err != nil {
		return nil, nil, err
	}
	if !principal.IsBound() {
		return nil, nil, fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
	}
	if firstSequence == 0 {
		firstSequence = 1
	}
	if contextstate.Exceeds(len(messages), contextstate.CurrentLimits().CommitEvents) {
		return nil, nil, fmt.Errorf("%w: turn contains too many source messages", contextstate.ErrInvalidDTO)
	}
	events := make([]contextstate.SourceEvent, 0, len(messages))
	payloads := make([]contextstate.PayloadRecord, 0, len(messages))
	for _, message := range messages {
		if err := sourceMessageRole(message.Role); err != nil {
			return nil, nil, err
		}
		if message.Role == provider.RoleSystem {
			continue
		}
		id, err := contextstate.NewSourceID(principal.SessionID, firstSequence+uint64(len(events)))
		if err != nil {
			return nil, nil, err
		}
		event := contextstate.SourceEvent{
			ID: id, Kind: sourceKind(message), Role: message.Role,
			ToolCallID: message.ToolCallID, Provenance: "host-turn",
			RedactionStatus: "metadata",
		}
		if message.Content != "" {
			payload, err := contextstate.SanitizeSourcePayload(ctx, principal, []byte(message.Content), policy)
			if err != nil {
				return nil, nil, err
			}
			event.PayloadRef = payload.Ref.Ref
			event.Size = payload.Ref.Size
			if payload.Dereferenceable {
				event.RedactionStatus = "sanitized"
			}
			payloads = append(payloads, contextstate.PayloadRecord{
				Ref: payload.Ref, Retention: payload.Retention, Data: append([]byte(nil), payload.Bytes...),
			})
		}
		if err := event.Validate(); err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, nil, fmt.Errorf("%w: turn has no persistable source messages", contextstate.ErrInvalidDTO)
	}
	return events, payloads, nil
}

func sourceMessageRole(role string) error {
	switch role {
	case provider.RoleSystem, provider.RoleUser, provider.RoleAssistant, provider.RoleTool:
		return nil
	default:
		return fmt.Errorf("%w: source message role %q is not allowlisted", contextstate.ErrInvalidDTO, role)
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
