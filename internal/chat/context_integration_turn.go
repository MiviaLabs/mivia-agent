package chat

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// adoptInterruptedPlainTurn keeps the user's message and the already-streamed
// partial answer of an interrupted legacy plain turn (sendPlainLegacy): adopt
// both into the session and persist, then hand the partial back instead of the
// error. Only a still-current turn may persist (stale-turn fence); otherwise it
// returns ok=false and the caller keeps today's drop-everything error path.
// A save failure returns with the partial reply.
func (s *Session) adoptInterruptedPlainTurn(ctx context.Context, err error, snapshot plainTurnSnapshot, prepared []provider.Message, persistedText, partial string) (string, bool, error) {
	if !isInterruptedTurn(ctx, err) || !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		return "", false, nil
	}
	s.mu.Lock()
	replaceNewestUserText(prepared, snapshot.messages[len(snapshot.messages)-1].Content, persistedText)
	s.Messages = prepared
	if strings.TrimSpace(partial) != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: partial, CreatedAt: time.Now()})
	}
	s.mu.Unlock()
	return partial, true, s.persistPlainLegacyTurn(snapshot.token)
}

func (s *Session) persistPlainLegacyTurn(token OperationToken) error {
	return plainPersistenceError(s.saveAfterTurn(token))
}

func plainPersistenceError(err error) error {
	if errors.Is(err, ErrStaleOperation) || errors.Is(err, ErrStaleAutosave) {
		return nil
	}
	return err
}

// commitInterruptedPlainContext handles the errored ChatStream path of a plain
// context turn (sendPlainContext). An interrupted turn (Ctrl+C / force-send /
// deadline) must not lose the user's message or the answer already streamed:
// publish the partial turn durably with OutcomeCancelled under an uncanceled
// context (mirroring commitContextTurn), guarded by the same
// plainTurnCurrent/token fence as the success path, then return the partial
// instead of the error. Non-interrupted errors keep today's
// discard-and-drop behavior.
func (s *Session) commitInterruptedPlainContext(ctx context.Context, err error, snapshot plainTurnSnapshot, prepared []provider.Message, persistedText, partial string, preparation contextmgr.Preparation, summary injectedSummary) (string, error) {
	if isInterruptedTurn(ctx, err) && s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		s.contextPublishMu.Lock()
		if s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
			candidate := cloneContextMessages(prepared)
			userText := snapshot.messages[len(snapshot.messages)-1].Content
			replaceNewestUserText(candidate, userText, persistedText)
			if strings.TrimSpace(partial) != "" {
				candidate = append(candidate, provider.Message{Role: provider.RoleAssistant, Content: partial, CreatedAt: time.Now()})
			}
			ordered := contextTurnMessages(candidate, userText)
			commitCtx := context.Background()
			result, commitErr := buildContextTurnResult(commitCtx, snapshot.context, &preparation, candidate, ordered, snapshot.myTurn)
			if commitErr == nil {
				result.Active = summary.appendTo(result.Active)
				candidate = summary.appendTo(candidate)
				result.Outcome = contextmgr.OutcomeCancelled
				commitErr = snapshot.context.manager.Commit(commitCtx, preparation, result)
			}
			if commitErr == nil {
				s.mu.Lock()
				current := s.tokenCurrentLocked(snapshot.token)
				if current {
					s.Messages = candidate
					s.contextHead = nextContextRevision(preparation, result)
				}
				s.mu.Unlock()
				if !current {
					// The commit succeeded durably but the in-memory fence
					// drifted during commit I/O: re-sync so the session is
					// not wedged (same recovery as commitContextTurn).
					_ = s.resyncContextHead()
				}
				snapshot.context.manager.PreparationManager.Discard(preparation)
				s.contextPublishMu.Unlock()
				s.emitContextCompaction(preparation, snapshot.myTurn, summary.present)
				return partial, nil
			}
			snapshot.context.manager.PreparationManager.Discard(preparation)
			s.contextPublishMu.Unlock()
			return "", commitErr
		}
		snapshot.context.manager.PreparationManager.Discard(preparation)
		s.contextPublishMu.Unlock()
	}
	snapshot.context.manager.PreparationManager.Discard(preparation)
	return "", err
}

// commitPlainContextTurn persists a successfully streamed plain context turn
// (sendPlainContext success path): commit the candidate durably under the
// contextPublishMu fence, then adopt it into the session when the operation
// token is still current. Stale turns, commit failures, and token drift keep
// today's exact return semantics (reply/no-op, error, or ErrStaleOperation).
func (s *Session) commitPlainContextTurn(ctx context.Context, reply string, snapshot plainTurnSnapshot, prepared []provider.Message, persistedText string, preparation contextmgr.Preparation, summary injectedSummary) (string, error) {
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, nil
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, nil
	}
	candidate := cloneContextMessages(prepared)
	userText := snapshot.messages[len(snapshot.messages)-1].Content
	replaceNewestUserText(candidate, userText, persistedText)
	if strings.TrimSpace(reply) != "" {
		candidate = append(candidate, provider.Message{Role: provider.RoleAssistant, Content: reply, CreatedAt: time.Now()})
	}
	ordered := contextTurnMessages(candidate, userText)
	result, err := buildContextTurnResult(ctx, snapshot.context, &preparation, candidate, ordered, snapshot.myTurn)
	if err == nil {
		// The summary joins the committed active context and the live history,
		// but never `ordered` above: it is host-generated with no source event
		// of its own, so the checkpoint is its only durable carrier.
		result.Active = summary.appendTo(result.Active)
		candidate = summary.appendTo(candidate)
		err = snapshot.context.manager.Commit(ctx, preparation, result)
	}
	if err != nil {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	s.mu.Lock()
	if !s.tokenCurrentLocked(snapshot.token) {
		s.mu.Unlock()
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, ErrStaleOperation
	}
	s.Messages = candidate
	s.contextHead = nextContextRevision(preparation, result)
	s.mu.Unlock()
	snapshot.context.manager.PreparationManager.Discard(preparation)
	s.emitContextCompaction(preparation, snapshot.myTurn, summary.present)
	return reply, nil
}
