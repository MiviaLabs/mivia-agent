package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// finishAgentTurn adopts a completed turn's history, commits it durably, and
// only then publishes any tool admission the turn staged. The ordering is the
// point: the generation bump a publication performs would fence this turn out
// of its own persistence if it happened first (plan tools/05 D6).
func (s *Session) finishAgentTurn(ctx context.Context, loop *agent.Loop, registry *tools.Registry, userText, persistedText string, token OperationToken, turn *TurnOptions, contextCfg contextTurnConfig, turnErr error) error {
	// The no-op streak is a within-turn loop detector; the boundary ends the
	// turn it was counting.
	s.resetAdmissionNoOps()
	s.adoptCalibration(loop.Calibration)
	agent.ScrubEphemeralToolMessages(loop.Messages, registry)
	// Repair reasoning-less tool-call exchanges ONCE, here, at turn adoption -
	// not on every later request serialization. Without this, a provider that
	// rejects such exchanges (DeepSeek) has toAPIMessages silently re-rewrite
	// persisted history on every request, breaking the prompt-cache prefix and
	// hiding context with no persisted trace. See
	// provider.RepairReasoningLessToolExchanges.
	if policy := provider.ReasoningPolicyFor(loop.Completer); policy.RejectReasoningLess {
		loop.Messages = provider.RepairReasoningLessToolExchanges(loop.Messages)
	}
	// Drop any assistant turn carrying neither content nor tool calls - the
	// shape a provider's genuinely empty response leaves behind. Unlike the
	// reasoning-less repair above, this is unconditional: every provider can
	// return an empty response, and provider.ValidateToolPairing (which
	// gates every later Prepare()/commit through contextmgr's planner) hard-
	// rejects this exact shape rather than silently dropping it the way
	// toAPIMessages does at the wire layer. Left in persisted history, it
	// poisons every subsequent turn's context preparation, not just the one
	// that produced it. See provider.DropEmptyAssistantTurns.
	loop.Messages = provider.DropEmptyAssistantTurns(loop.Messages)
	replaceNewestUserText(loop.Messages, userText, persistedText)
	if contextCfg.manager != nil {
		return s.finishContextTurn(ctx, loop, userText, token, turn, contextCfg, turnErr)
	}
	persistErr := s.commitPreparedTurn(loop.Messages, token, turnErr)
	if persistErr == nil {
		s.PublishPendingAdmission()
	} else {
		s.dropPendingAdmissionForTurn(token.TurnID)
	}
	s.runTurnCleanup(turn)
	s.clearLiveTurnToken()
	return persistErr
}

// finishContextTurn is the durable-context path: build the turn result, commit
// it, adopt the new head, and publish a staged admission only on the branch
// where all of that succeeded.
func (s *Session) finishContextTurn(ctx context.Context, loop *agent.Loop, userText string, token OperationToken, turn *TurnOptions, contextCfg contextTurnConfig, turnErr error) error {
	interrupted := isInterruptedTurn(ctx, turnErr)
	if turnErr != nil && !interrupted {
		return s.finishErroredContextTurn(ctx, loop, userText, token, turn, contextCfg, turnErr)
	}
	if !loop.HasPreparation {
		s.dropPendingAdmissionForTurn(token.TurnID)
		s.runTurnCleanup(turn)
		s.clearLiveTurnToken()
		if loop.PreparationErr != nil {
			return loop.PreparationErr
		}
		return fmt.Errorf("%w: agent completed without a preparation", contextstate.ErrCheckpointConflict)
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.RLock()
	current := s.tokenCurrentLocked(token)
	s.mu.RUnlock()
	if !current {
		contextCfg.manager.PreparationManager.Discard(loop.LastPreparation)
		// A superseded turn never publishes an admission (R2-1).
		s.dropPendingAdmissionForTurn(token.TurnID)
		s.runTurnCleanup(turn)
		s.clearLiveTurnToken()
		return ErrStaleOperation
	}
	outcome := contextmgr.OutcomeComplete
	if interrupted {
		outcome = contextmgr.OutcomeCancelled
	}
	err := s.commitContextTurn(ctx, loop, userText, token, contextCfg, outcome)
	if err != nil {
		// The durable commit failed: the staging turn never committed, so its
		// staged admission must not survive to publish at a later boundary
		// (plan tools/05 D7 Commit-failure branch). Mirrors the legacy
		// persistErr != nil drop above.
		s.dropPendingAdmissionForTurn(token.TurnID)
	}
	s.runTurnCleanup(turn)
	s.clearLiveTurnToken()
	return err
}

// finishErroredContextTurn handles a turn that ended in a non-interrupted
// error. It used to discard the whole turn - history, staged admission, and
// all - unconditionally; this made resume lose the user's prompt and every
// tool call the turn made whenever a provider error struck, and disagreed
// with the legacy (non-context) path's commitPreparedTurn, which already
// persists an errored turn's history and publishes its admission whenever the
// persistence itself succeeds. This brings the context-store path in line
// with that existing, shipped behavior instead of preserving an
// inconsistency between the two backends.
//
// The defer runs turn cleanup and clears the live-turn token exactly once, on
// every branch below, mirroring the discard branch's original contract.
//
// Return contract: this function must always return nil or ErrStaleOperation,
// never turnErr and never the commit's own error. sendAgent (session.go)
// already surfaces the original turnErr independently of this return value,
// and only overrides it when finishAgentTurn returns a non-nil, non-stale
// error - so returning anything else here would either mask turnErr behind an
// unrelated persistence error, or double-report the same failure.
func (s *Session) finishErroredContextTurn(ctx context.Context, loop *agent.Loop, userText string, token OperationToken, turn *TurnOptions, contextCfg contextTurnConfig, turnErr error) error {
	defer func() {
		s.runTurnCleanup(turn)
		s.clearLiveTurnToken()
	}()
	if errors.Is(turnErr, agent.ErrPromptBudgetExceeded) {
		// Unreachable via the durable-context path today - a configured
		// PreparationManager makes sdkPromptBudgetPreflight (the only source of
		// this error) a no-op - but kept as defense-in-depth rather than
		// assumed away, at zero cost.
		if loop.HasPreparation {
			contextCfg.manager.PreparationManager.Discard(loop.LastPreparation)
		}
		s.dropPendingAdmissionForTurn(token.TurnID)
		return nil
	}
	if !loop.HasPreparation {
		// The turn died before a preparation was ever built: there is nothing
		// to fingerprint a checkpoint against, so no durable commit is
		// attempted. Best effort instead: adopt what history exists into the
		// rolling snapshot, so a later resume has somewhere newer than the
		// last complete checkpoint to fall back to.
		s.adoptFailedTurnSnapshot(loop, token)
		s.dropPendingAdmissionForTurn(token.TurnID)
		return nil
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.RLock()
	current := s.tokenCurrentLocked(token)
	s.mu.RUnlock()
	if !current {
		contextCfg.manager.PreparationManager.Discard(loop.LastPreparation)
		s.dropPendingAdmissionForTurn(token.TurnID)
		return ErrStaleOperation
	}
	if err := s.commitContextTurn(ctx, loop, userText, token, contextCfg, contextmgr.OutcomeUpstreamErr); err != nil {
		// The commit itself failed - most commonly BuildCommitRequest's
		// message-shape validation rejecting a turn that died mid-tool-call
		// (a dangling tool_use with no paired result). Fall back to the
		// original discard behavior rather than surfacing a second, unrelated
		// persistence error on top of turnErr, which the caller already has.
		s.dropPendingAdmissionForTurn(token.TurnID)
	}
	return nil
}

// adoptFailedTurnSnapshot adopts loop.Messages into live session state with token
// fencing like commitPreparedTurn, then saves the rolling snapshot for resumes.
//
// This is the sole errored-turn durable write bypassing commitContextTurn's
// validateMessageShape/ValidateToolPairing gate. It validates candidate messages
// directly so invalid history does not persist and poison future Prepare() calls.
// On validation failure, it acts like commitContextTurn discard: the turn drops,
// and s.Messages and durable snapshots remain unchanged.
//
// DropEmptyAssistantTurns already strips trailing empty assistant messages, but
// ValidateToolPairing still guards against dangling tool_use, orphan tool results,
// duplicate call IDs, and unmediated future call paths.
func (s *Session) adoptFailedTurnSnapshot(loop *agent.Loop, token OperationToken) {
	candidate := cloneContextMessages(loop.Messages)
	if err := validateRestoredMessages(candidate); err != nil {
		return
	}
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return
	}
	s.Messages = candidate
	s.mu.Unlock()
	s.SaveAfterTurn()
}

// commitContextTurn performs the durable publication for one context turn and
// adopts its result in memory. The caller holds contextPublishMu. outcome is
// the checkpoint's contextmgr.Outcome* tag - OutcomeComplete for an ordinary
// successful turn, OutcomeCancelled for an interrupted one, OutcomeUpstreamErr
// for a non-interrupted error the caller still wants committed.
func (s *Session) commitContextTurn(ctx context.Context, loop *agent.Loop, userText string, token OperationToken, contextCfg contextTurnConfig, outcome string) error {
	ordered := contextTurnMessages(loop.Messages, userText)
	preparation := loop.LastPreparation
	commitCtx := ctx
	if outcome == contextmgr.OutcomeCancelled {
		// The provider context is canceled by force-send, but the durable
		// history publication must still complete before the next turn starts.
		commitCtx = context.Background()
	}
	result, err := buildContextTurnResult(commitCtx, contextCfg, &preparation, loop.Messages, ordered, token.TurnID)
	// A compacting turn showed the model a summary of the messages it dropped,
	// but only inside its own ephemeral requests. The dropped messages are
	// gone for good, so unless the summary joins the committed active context
	// it disappears at this boundary and every later turn sees a truncated
	// history with no account of what was removed. Manual /compact already
	// carries its summary across for the same reason: the message is
	// host-generated with no source event of its own, so the checkpoint is its
	// only durable carrier. It is appended to Active only - never to `ordered`,
	// which feeds source projection.
	summaryMessage, haveSummary := loop.InjectedSummary()
	// The committed copies of the rendered summary are ANONYMOUS: the wire
	// Name marks ephemeral request-path injections (a host hint), and every
	// restore path runs provider.ValidateToolPairing, which refuses NAMED user
	// messages - a named summary made the session unresumable after one more
	// turn. The loop's own ephemeral request injection keeps the Name; only
	// the copies appended below strip it (provider.Message is a struct value,
	// so mutating this local copy cannot touch the loop's).
	summaryMessage.Name = ""
	if err == nil && haveSummary {
		result.Active = append(cloneContextMessages(result.Active), summaryMessage)
	}
	if err == nil {
		result.Outcome = outcome
	}
	if err == nil {
		err = contextCfg.manager.Commit(commitCtx, preparation, result)
	}
	contextCfg.manager.PreparationManager.Discard(preparation)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		// The commit succeeded durably but the in-memory fence drifted (e.g.
		// SetPromptBudget bumped the epoch during commit I/O). Re-sync from the
		// store so the session isn't permanently wedged.
		_ = s.resyncContextHead()
		return nil
	}
	s.Messages = cloneContextMessages(loop.Messages)
	if haveSummary {
		// Same message, same reason, in the live history the next turn builds
		// its request from. Kept in step with result.Active above so the
		// in-memory and durable views of this turn do not diverge.
		s.Messages = append(s.Messages, summaryMessage)
	}
	// Belt-and-braces: the planner preserves the core-memory frame
	// (PreserveNames), and this guard re-places it from the session mirror if
	// it was ever dropped, so a compacted turn can never strip the promoted
	// memory facts from the live session (BUG 3).
	reseedMemoryFrameLocked(s)
	s.contextHead = nextContextRevision(preparation, result)
	s.mu.Unlock()
	if !loop.TurnCompactionEmitted() {
		reason := summaryUnavailableReason(contextCfg, haveSummary, loop.SummaryFailureReason())
		s.emitContextCompaction(commitCtx, contextCfg, preparation, token.TurnID, haveSummary, reason)
	}
	// Durably committed under this turn's own still-valid fence, so the
	// generation bump below cannot fence the turn out of its own persistence
	// (plan tools/05 D6 ordering). Publication happens whenever the commit
	// succeeded, regardless of outcome (OutcomeUpstreamErr included): if the
	// turn's history is durably committed, the admission decision made
	// against that history is committed too. This matches the legacy
	// (non-context) path's commitPreparedTurn, which already publishes
	// admission on any successful persistence regardless of turnErr - the two
	// backends must not disagree on this.
	s.PublishPendingAdmission()
	return nil
}

// commitPreparedTurn adopts a finished turn's history and persists it only
// while the captured operation fence remains current. Errored and cancelled
// turns still preserve the visible partial history.
func (s *Session) commitPreparedTurn(msgs []provider.Message, token OperationToken, turnErr error) error {
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	if !errors.Is(turnErr, agent.ErrPromptBudgetExceeded) {
		s.Messages = msgs
	}
	s.mu.Unlock()
	return s.saveAfterTurn(token)
}

// clearLiveTurnToken zeroes the step-boundary fence token once the turn has
// finished and its commit/drop decision is made, so a stale token cannot leak
// into the next turn's commit fence (w2d: liveTurnToken is a per-turn token).
// Written under mu to match the write discipline of the other session fields.
func (s *Session) clearLiveTurnToken() {
	s.mu.Lock()
	s.liveTurnToken = OperationToken{}
	s.mu.Unlock()
}
