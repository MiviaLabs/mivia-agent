package chat

import (
	"context"
	"errors"
	"fmt"
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
		logStaleOperation("plain turn commit", err)
		return nil
	}
	return err
}

// plainCommitPersistenceError tags a durable-commit failure on the plain
// context path as a persistence failure while keeping the original cause
// matchable: errors.Is(err, ErrPersistence) and errors.Is(err, <cause>) both
// hold (multi-%w). Returns nil for a nil cause. This is what makes
// shouldPrintOneShotOutput (internal/clichat/chat.go) print the answer that
// already streamed to the caller's writer instead of suppressing it - the
// returned error string/value itself is not what reaches the terminal, the
// ErrPersistence tag on it is what flips that decision.
func plainCommitPersistenceError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrPersistence, err)
}

// adoptUncommittedPlainTurn adopts candidate into s.Messages under the
// operation fence WITHOUT advancing s.contextHead/s.contextRevision: the
// checkpoint did not land durably, so the durable head must stay where it
// is while the exchange survives in memory until the next successful
// commit catches it up. In-memory history now runs one turn ahead of
// contextHead until that catch-up happens - this is intended, not drift.
// Refuses adoption (no-op) if the turn's fence has gone stale, or if
// validateRestoredMessages rejects candidate's shape (fail closed - do not
// poison later Prepare calls with an unpairable history).
//
// Every call site already holds contextPublishMu when this runs; this
// function only ever acquires s.mu, preserving the established
// contextPublishMu -> s.mu lock order.
func (s *Session) adoptUncommittedPlainTurn(candidate []provider.Message, snapshot plainTurnSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnID != snapshot.myTurn || !s.tokenCurrentLocked(snapshot.token) {
		return
	}
	if err := validateRestoredMessages(candidate); err != nil {
		return
	}
	s.Messages = candidate
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
				s.emitContextCompaction(commitCtx, snapshot.context, preparation, snapshot.myTurn, summary.present, summary.reason)
				return partial, nil
			}
			// The commit never landed durably, but the user's message and the
			// already-streamed partial answer are real: adopt them into
			// memory (candidate may already carry an appended summary here,
			// from buildContextTurnResult above) so the next successful
			// commit catches contextHead up, instead of losing the turn
			// outright. contextHead/contextRevision stay pinned to the last
			// durable revision - do not advance them here.
			s.adoptUncommittedPlainTurn(candidate, snapshot)
			snapshot.context.manager.PreparationManager.Discard(preparation)
			s.contextPublishMu.Unlock()
			return partial, plainCommitPersistenceError(commitErr)
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
		// rejecting a turn that died mid-stream in an unpairable state. Adopt
		// the exchange into memory anyway (candidate may already carry an
		// appended summary here) so it is not silently lost - the next
		// successful commit catches contextHead up.
		//
		// Deliberately return err UNWRAPPED here, not tagged with
		// ErrPersistence: this is the non-interrupted-error path, so the
		// stream itself failed upstream, not just the durable save. The
		// buffered/partial text is incomplete and untrustworthy, and tagging
		// it ErrPersistence would wrongly signal "the answer is fine, only
		// the save failed" when the answer itself never finished streaming.
		// shouldPrintOneShotOutput (internal/clichat/chat.go) must stay false
		// for this case.
		s.adoptUncommittedPlainTurn(candidate, snapshot)
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
	s.emitContextCompaction(ctx, snapshot.context, preparation, snapshot.myTurn, summary.present, summary.reason)
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
		// Durable commit failed, but the exchange already happened (and
		// candidate may already carry an appended summary here, if this
		// failure came from Commit rather than result construction): adopt
		// it into memory now rather than losing it, so it survives until the
		// next successful commit catches contextHead up.
		// contextHead/contextRevision stay pinned to the last durably
		// committed revision - do not advance them here. reply is the
		// model's actual streamed answer; return it un-blanked so Session's
		// own SendUser contract holds for direct callers, tests, and future
		// callers that read it (one-shot mode's answer already reached the
		// terminal via the streamed writer, not via this return value - see
		// plainCommitPersistenceError).
		s.adoptUncommittedPlainTurn(candidate, snapshot)
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, plainCommitPersistenceError(err)
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
	s.emitContextCompaction(ctx, snapshot.context, preparation, snapshot.myTurn, summary.present, summary.reason)
	return reply, nil
}
