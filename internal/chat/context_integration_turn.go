package chat

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// adoptInterruptedPlainTurn keeps the user's message and the already-streamed
// partial answer of an interrupted legacy plain turn (sendPlainLegacy): adopt
// both into the session and persist, then hand the partial back instead of the
// error. Only a still-current turn may persist (stale-turn fence); otherwise it
// returns ok=false and the caller keeps today's drop-everything error path.
// A save failure returns with the partial reply. The fence check happens
// INSIDE the same lock that performs the write, not as a separate
// plainTurnCurrent call before acquiring the lock: a check-then-separately-
// lock shape leaves a window where a concurrent /clear (which bumps
// s.turnID and resets s.Messages under its own s.mu.Lock(), specifically to
// fence out an in-flight turn - see session_reset.go's resetSystem) can
// complete between the check and the write, and an unconditional write
// afterward would silently resurrect the pre-clear history the user just
// cleared - the same TOCTOU a bug audit caught in adoptErroredPlainTurn
// (commit 29b25da3), fixed here the same way. isInterruptedTurn itself
// carries no session state, so it stays outside the lock; only the
// session-state check moves in.
func (s *Session) adoptInterruptedPlainTurn(ctx context.Context, err error, snapshot plainTurnSnapshot, prepared []provider.Message, persistedText, partial string) (string, bool, error) {
	if !isInterruptedTurn(ctx, err) {
		return "", false, nil
	}
	s.mu.Lock()
	if s.turnID != snapshot.myTurn || !s.tokenCurrentLocked(snapshot.token) {
		s.mu.Unlock()
		return "", false, nil
	}
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

// adoptErroredPlainTurn best-effort persists a non-interrupted legacy plain
// turn's history (the user's message, and any already-streamed partial
// reply) as a pure side effect - it never changes what the caller returns.
// Unlike adoptInterruptedPlainTurn, a real provider error must still surface
// to the caller exactly as before; this only prevents that history from
// vanishing on the next resume. The fence check happens INSIDE the same lock
// that performs the write, not as a separate plainTurnCurrent call before
// acquiring the lock: a check-then-separately-lock shape leaves a window
// where a concurrent /clear (which bumps s.turnID and resets s.Messages
// under its own s.mu.Lock()) can complete between the check and the write,
// and an unconditional write afterward would silently resurrect the
// pre-clear history the user just cleared. commitErroredPlainContext,
// commitInterruptedPlainContext, and commitPlainContextTurn all already
// re-check their fence inside the locked section for the same reason; this
// mirrors that pattern. Any persistence failure is discarded rather than
// compounding the original error the caller already has.
func (s *Session) adoptErroredPlainTurn(snapshot plainTurnSnapshot, prepared []provider.Message, persistedText, partial string) {
	s.mu.Lock()
	if s.turnID != snapshot.myTurn || !s.tokenCurrentLocked(snapshot.token) {
		s.mu.Unlock()
		return
	}
	replaceNewestUserText(prepared, snapshot.messages[len(snapshot.messages)-1].Content, persistedText)
	s.Messages = prepared
	if strings.TrimSpace(partial) != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: partial, CreatedAt: time.Now()})
	}
	s.mu.Unlock()
	_ = s.persistPlainLegacyTurn(snapshot.token)
}

// appendCommitted appends the rendered context summary to a durable artifact
// (the committed checkpoint's active context or the live history) with the
// wire Name stripped. The Name marks ephemeral request-path injections (a
// host hint), and every restore path runs provider.ValidateToolPairing, which
// refuses NAMED user messages - a named summary made the session unresumable
// after one more turn. The request-path copy keeps the Name; only these
// committed copies strip it. Mirrors summarizeManualCompact (manual /compact)
// and commitContextTurn (AUTO).
func (s injectedSummary) appendCommitted(messages []provider.Message) []provider.Message {
	out := s.appendTo(messages)
	if s.present {
		out[len(out)-1].Name = ""
	}
	return out
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
				result.Active = summary.appendCommitted(result.Active)
				candidate = summary.appendCommitted(candidate)
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
				s.emitContextCompaction(commitCtx, snapshot.context, preparation, snapshot.myTurn, summary.present)
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

// commitErroredPlainContext handles a non-interrupted error from a plain
// context turn's ChatStream call. Unlike commitInterruptedPlainContext, the
// original error must still surface to the caller - a real provider failure
// must not look like a quiet success - so this function's return value is
// always ("", err) regardless of whether the best-effort commit below
// succeeds, fails, or is skipped. The commit is a pure side effect: it exists
// only so the user's question (and any already-streamed partial reply)
// survive on resume instead of vanishing, mirroring
// finishErroredContextTurn on the agent/tools path (turn_finish.go).
func (s *Session) commitErroredPlainContext(ctx context.Context, err error, snapshot plainTurnSnapshot, prepared []provider.Message, persistedText, partial string, preparation contextmgr.Preparation, summary injectedSummary) (string, error) {
	if errors.Is(err, agent.ErrPromptBudgetExceeded) {
		// Defense-in-depth: an over-budget history must never be committed.
		// Unreachable today via this call site (Prepare's own budget check
		// returns before ChatStream ever runs), mirrored from
		// finishErroredContextTurn's identical guard.
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	candidate := cloneContextMessages(prepared)
	userText := snapshot.messages[len(snapshot.messages)-1].Content
	replaceNewestUserText(candidate, userText, persistedText)
	if strings.TrimSpace(partial) != "" {
		candidate = append(candidate, provider.Message{Role: provider.RoleAssistant, Content: partial, CreatedAt: time.Now()})
	}
	ordered := contextTurnMessages(candidate, userText)
	result, commitErr := buildContextTurnResult(ctx, snapshot.context, &preparation, candidate, ordered, snapshot.myTurn)
	if commitErr == nil {
		result.Active = summary.appendCommitted(result.Active)
		candidate = summary.appendCommitted(candidate)
		result.Outcome = contextmgr.OutcomeUpstreamErr
		commitErr = snapshot.context.manager.Commit(ctx, preparation, result)
	}
	snapshot.context.manager.PreparationManager.Discard(preparation)
	if commitErr != nil {
		// The commit itself failed - most commonly message-shape validation
		// rejecting a turn that died mid-stream in an unpairable state. Fall
		// back to the original discard behavior rather than surfacing a
		// second, unrelated persistence error on top of err.
		return "", err
	}
	s.mu.Lock()
	current := s.tokenCurrentLocked(snapshot.token)
	if current {
		s.Messages = candidate
		s.contextHead = nextContextRevision(preparation, result)
	}
	s.mu.Unlock()
	if !current {
		// The commit succeeded durably but the in-memory fence drifted during
		// commit I/O: re-sync so the session is not wedged (same recovery as
		// commitContextTurn/commitInterruptedPlainContext).
		_ = s.resyncContextHead()
	}
	s.emitContextCompaction(ctx, snapshot.context, preparation, snapshot.myTurn, summary.present)
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
		result.Active = summary.appendCommitted(result.Active)
		candidate = summary.appendCommitted(candidate)
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
	s.emitContextCompaction(ctx, snapshot.context, preparation, snapshot.myTurn, summary.present)
	return reply, nil
}
