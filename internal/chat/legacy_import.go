package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// LegacyImportSink is the transactional destination for an import. The sink
// receives fully validated, sanitized records only after the legacy session
// has been read and converted in memory.
type LegacyImportSink interface {
	ImportSource(context.Context, contextstate.Principal, string, string, []contextstate.SourceEvent, []contextstate.PayloadRecord) (contextstate.ImportResult, error)
}

type LegacyImporter struct {
	legacy  *FileSessionStore
	sink    LegacyImportSink
	policy  contextstate.RedactionPolicy
	mu      sync.Mutex
	results map[string]contextstate.ImportResult
}

func NewLegacyImporter(legacy *FileSessionStore, sink LegacyImportSink, policy contextstate.RedactionPolicy) (*LegacyImporter, error) {
	if legacy == nil || sink == nil {
		return nil, fmt.Errorf("%w: legacy store and import sink are required", contextstate.ErrInvalidDTO)
	}
	return &LegacyImporter{legacy: legacy, sink: sink, policy: policy, results: make(map[string]contextstate.ImportResult)}, nil
}

func (i *LegacyImporter) Import(ctx context.Context, principal contextstate.Principal, legacySession, operationKey string) (contextstate.ImportResult, error) {
	if err := validateLegacyImportInput(principal, legacySession, operationKey); err != nil {
		return contextstate.ImportResult{}, err
	}
	cacheKey := principal.WorkspaceID + "\x00" + principal.SessionID + "\x00" + principal.SubjectID + "\x00" + legacySession + "\x00" + operationKey
	i.mu.Lock()
	if result, ok := i.results[cacheKey]; ok {
		i.mu.Unlock()
		return result, nil
	}
	i.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contextstate.ImportResult{}, err
	}
	msgs, _, err := i.legacy.LoadWithInfo(legacySession)
	if err != nil {
		return contextstate.ImportResult{}, err
	}
	if len(msgs) == 0 || contextstate.Exceeds(len(msgs), contextstate.CurrentLimits().CommitEvents) {
		return contextstate.ImportResult{}, fmt.Errorf("%w: legacy session has no bounded message set", contextstate.ErrInvalidDTO)
	}
	events, payloads, err := i.convertLegacyMessages(ctx, principal, msgs)
	if err != nil {
		return contextstate.ImportResult{}, err
	}
	rng, err := contextstate.NewSourceRange(events[0].ID, events[len(events)-1].ID)
	if err != nil {
		return contextstate.ImportResult{}, err
	}
	digest, err := importDigest(events, payloads)
	if err != nil {
		return contextstate.ImportResult{}, err
	}
	result, err := i.sink.ImportSource(ctx, principal, legacySession, operationKey, events, payloads)
	if err != nil {
		return contextstate.ImportResult{}, err
	}
	if result.SessionID == "" {
		result.SessionID = principal.SessionID
	}
	if result.SourceRange == (contextstate.SourceRange{}) {
		result.SourceRange = rng
	}
	if result.IdempotencyKey == "" {
		result.IdempotencyKey = operationKey
	}
	if result.Rollback.Digest == "" {
		result.Rollback = contextstate.RollbackToken{SessionID: principal.SessionID, IdempotencyKey: operationKey, Digest: digest}
	}
	i.mu.Lock()
	i.results[cacheKey] = result
	i.mu.Unlock()
	return result, nil
}

func (i *LegacyImporter) convertLegacyMessages(ctx context.Context, principal contextstate.Principal, msgs []provider.Message) ([]contextstate.SourceEvent, []contextstate.PayloadRecord, error) {
	events := make([]contextstate.SourceEvent, 0, len(msgs))
	payloads := make([]contextstate.PayloadRecord, 0, len(msgs))
	for index, message := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !validLegacyRole(message.Role) {
			return nil, nil, fmt.Errorf("%w: unsupported legacy role", contextstate.ErrInvalidDTO)
		}
		id, _ := contextstate.NewSourceID(principal.SessionID, uint64(index+1))
		event := contextstate.SourceEvent{ID: id, Kind: "message", Role: message.Role, ToolCallID: message.ToolCallID, Provenance: "legacy", RedactionStatus: "metadata", Size: len(message.Content)}
		if message.Content != "" {
			payload, err := contextstate.SanitizeSourcePayload(ctx, principal, []byte(message.Content), i.policy)
			if err != nil {
				return nil, nil, err
			}
			payloads = append(payloads, contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes})
			event.PayloadRef = payload.Ref.Ref
			if payload.Dereferenceable {
				event.RedactionStatus = "sanitized"
			} else {
				event.RedactionStatus = "hash-only"
			}
		}
		if err := event.Validate(); err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	return events, payloads, nil
}

func validateLegacyImportInput(principal contextstate.Principal, session, key string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if !principal.IsBound() {
		return contextstate.ErrPrincipalMismatch
	}
	if session == "" || sanitizeSessionName(session) != session || strings.ContainsAny(session, `/\\`) {
		return fmt.Errorf("%w: invalid authorized legacy session handle", contextstate.ErrInvalidDTO)
	}
	if strings.TrimSpace(key) == "" || len(key) > contextstate.MaxIdentifierBytes {
		return fmt.Errorf("%w: invalid import operation key", contextstate.ErrInvalidDTO)
	}
	return nil
}

func validLegacyRole(role string) bool {
	switch role {
	case provider.RoleSystem, provider.RoleUser, provider.RoleAssistant, provider.RoleTool:
		return true
	default:
		return false
	}
}

func importDigest(events []contextstate.SourceEvent, payloads []contextstate.PayloadRecord) (string, error) {
	data, err := contextstate.MarshalCanonical(struct {
		Events   []contextstate.SourceEvent   `json:"events"`
		Payloads []contextstate.PayloadRecord `json:"payloads"`
	}{events, payloads})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
