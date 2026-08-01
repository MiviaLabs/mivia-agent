package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func buildContextTurnResult(ctx context.Context, cfg contextTurnConfig, preparation *contextmgr.Preparation, active []provider.Message, ordered []provider.Message, turnID uint64) (contextmgr.TurnResult, error) {
	if cfg.manager == nil {
		return contextmgr.TurnResult{}, fmt.Errorf("%w: context manager is missing", contextstate.ErrCheckpointConflict)
	}
	result := contextmgr.TurnResult{
		Active: cloneContextMessages(active), Ordered: cloneContextMessages(ordered),
		TurnID: turnID, Outcome: contextmgr.OutcomeComplete,
	}
	if len(ordered) > 0 && ordered[0].Role == provider.RoleUser {
		result.User = []provider.Message{ordered[0]}
	}
	for _, message := range ordered {
		switch message.Role {
		case provider.RoleAssistant:
			result.Assistant = append(result.Assistant, message)
		case provider.RoleTool:
			result.Tool = append(result.Tool, message)
		}
	}
	if preparation == nil {
		return contextmgr.TurnResult{}, fmt.Errorf("%w: preparation is missing", contextstate.ErrCheckpointConflict)
	}
	events, payloads, err := contextmgr.ProjectSource(ctx, cfg.principal, ordered, preparation.Token.Revision.Source+1, cfg.redaction)
	if err != nil {
		return contextmgr.TurnResult{}, err
	}
	result.SourceEvents = events
	if len(events) > 0 {
		rangeValue, err := contextstate.NewSourceRange(events[0].ID, events[len(events)-1].ID)
		if err != nil {
			return contextmgr.TurnResult{}, err
		}
		preparation.Candidate.SourceRange = rangeValue
		preparation.Token.Range = rangeValue
	}
	preparation.Candidate.Payloads = payloads
	return result, nil
}

func nextContextRevision(preparation contextmgr.Preparation, result contextmgr.TurnResult) contextstate.Revision {
	return contextstate.Revision{
		Session: preparation.Token.Revision.Session + 1,
		Durable: preparation.Token.Revision.Durable + 1,
		Source:  preparation.Token.Revision.Source + uint64(len(result.SourceEvents)),
	}
}

func (s *Session) emitContextCompaction(preparation contextmgr.Preparation, turnID uint64) {
	s.mu.RLock()
	onEvent := s.OnAgentEvent
	bus := s.EventBus
	identityFactory := s.eventIdentity
	binding := s.binding
	sessionID := s.SessionID
	s.mu.RUnlock()
	var identity *events.Identity
	if identityFactory != nil {
		identity = identityFactory(binding.ModelGeneration)
	}
	agent.EmitCompaction(agent.Options{
		OnEvent: onEvent, EventBus: bus, SessionID: sessionID,
		TurnID: fmt.Sprintf("turn:%d", turnID), EventIdentity: identity,
	}, preparation)
}

func (s *Session) advanceContextHead(store contextstate.Store, principal contextstate.Principal, expected contextstate.Revision, expectedBinding, newBinding contextstate.BindingRevision, reason string, clearActive bool) error {
	request := contextstate.AdvanceRequest{
		OperationID: fmt.Sprintf("%s-%s-%d-%d-%d", reason, principal.SessionID, expected.Session, expected.Durable, newBinding.Generation),
		Principal:   principal, SessionID: principal.SessionID, Expected: expected,
		ExpectedBinding: expectedBinding, NewSession: expected.Session + 1,
		NewDurable: expected.Durable + 1, NewSourceSequence: expected.Source,
		NewBinding: newBinding, ClearActive: clearActive, Reason: reason,
	}
	return store.Advance(context.Background(), request)
}

// advanceBindingIfNeeded temporarily releases the session mutex for the
// durable CAS and returns with it reacquired so the caller can publish the
// already-validated binding atomically with the in-memory head.
func (s *Session) advanceBindingIfNeeded(enabled bool, store contextstate.Store, principal contextstate.Principal, expected contextstate.Revision, expectedBinding, newBinding contextstate.BindingRevision, reason string) error {
	if !enabled {
		return nil
	}
	s.mu.Unlock()
	err := s.advanceContextHead(store, principal, expected, expectedBinding, newBinding, reason, false)
	s.mu.Lock()
	return err
}
