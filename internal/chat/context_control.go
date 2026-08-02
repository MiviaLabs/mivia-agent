package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// ContextUsage is the live prompt estimate shown by chat surfaces.
type ContextUsage struct {
	UsedTokens          int
	BudgetTokens        int
	ContextWindowTokens int // model's full context window
	OutputReserveTokens int // output tokens reserved (max_output)
	Percent             int
}

// FormatTokenK formats a token count with a "k" suffix for values >= 1000,
// e.g. 200000 → "200k", 72000 → "72k", 999 → "999".
func FormatTokenK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// ContextUsage returns a prompt-cost estimate including tool schemas. The
// prompt budget already excludes the configured output reserve.
func (s *Session) ContextUsage() ContextUsage {
	s.mu.RLock()
	messages := cloneContextMessages(s.Messages)
	budget := s.MaxContextTokens
	if s.binding.PromptBudgetTokens > 0 {
		budget = s.binding.PromptBudgetTokens
	}
	window := s.binding.Profile.ContextWindowTokens
	outputReserve := 0
	if r := config.EffectiveOutputTokens(s.binding.Profile, s.MaxTokens); r != nil {
		outputReserve = *r
	}
	var toolSpecs []map[string]any
	if s.Tools != nil {
		toolSpecs = s.Tools.OpenAITools()
	}
	s.mu.RUnlock()
	used, err := provider.EstimatePromptCost(messages, toolSpecs)
	if err != nil {
		used = provider.MessagesTokens(messages)
	}
	percent := 0
	if budget > 0 {
		percent = used * 100 / budget
	}
	return ContextUsage{
		UsedTokens:          used,
		BudgetTokens:        budget,
		ContextWindowTokens: window,
		OutputReserveTokens: outputReserve,
		Percent:             percent,
	}
}

// ContextPreparation returns the preparation-only capability used by nested
// agents. It deliberately omits the checkpoint publisher and context store.
func (s *Session) ContextPreparation() (contextmgr.PreparationManager, contextmgr.PrepareInput, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.captureContextLocked()
	if cfg.manager == nil {
		return nil, contextmgr.PrepareInput{}, false
	}
	input := prepareInputForContext(s.Messages, s.MaxContextTokens, s.MaxTokens, s.binding, cfg.principal, cfg.policy)
	input.Revision = cfg.revision
	return cfg.manager.PreparationManager, input, true
}

// Compact prepares and durably publishes the current conversation immediately.
// It is serialized with turn publication and never sends another provider call.
func (s *Session) Compact(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()

	s.mu.RLock()
	if s.activeTurns > 0 {
		s.mu.RUnlock()
		return ErrStaleOperation
	}
	cfg := s.captureContextLocked()
	store := s.contextStore
	messages := cloneContextMessages(s.Messages)
	binding := s.binding
	turnID := s.turnID
	if turnID == 0 {
		turnID = 1
	}
	s.mu.RUnlock()
	if cfg.manager == nil || store == nil {
		return fmt.Errorf("context compaction is not configured")
	}
	input := prepareInputForContext(messages, s.MaxContextTokens, s.MaxTokens, binding, cfg.principal, cfg.policy)
	input.Revision = cfg.revision
	input.Force = true
	snapshot, err := store.Load(ctx, cfg.principal, cfg.principal.SessionID)
	if err != nil {
		return err
	}
	if snapshot.Revision != cfg.revision {
		return ErrStaleOperation
	}
	if snapshot.Revision.Source == 0 {
		return fmt.Errorf("nothing to compact: no conversation history")
	}
	input.SourceRange = snapshot.Active.SourceRange
	preparation, err := cfg.manager.PreparationManager.Prepare(ctx, input)
	if err != nil {
		return err
	}
	if !preparation.Compacted {
		cfg.manager.PreparationManager.Discard(preparation)
		return fmt.Errorf("context compaction made no reduction")
	}
	preparedMessages := cloneContextMessages(preparation.Messages)
	result := contextmgr.TurnResult{
		Active: preparedMessages, TurnID: turnID, Outcome: contextmgr.OutcomeComplete,
	}
	if err == nil {
		err = cfg.manager.Commit(ctx, preparation, result)
	}
	cfg.manager.PreparationManager.Discard(preparation)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.contextHead != cfg.revision || !s.contextEnabledLocked() {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	s.Messages = preparedMessages
	s.contextHead = nextContextRevision(preparation, result)
	s.mu.Unlock()
	s.emitContextCompaction(preparation, turnID)
	return nil
}

// RotateSessionID starts a fresh principal while retaining the configured
// context store. The new durable session is initialized before adoption.
func (s *Session) RotateSessionID() (string, error) {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.RLock()
	if s.activeTurns > 0 {
		s.mu.RUnlock()
		return "", ErrStaleOperation
	}
	manager := s.contextManager
	store := s.contextStore
	workspaceID, subjectID := s.contextPrincipal.WorkspaceID, s.contextPrincipal.SubjectID
	binding := captureBindingRevision(s.binding)
	oldID := s.SessionID
	s.mu.RUnlock()
	newID := runtime.NewSessionID()
	if manager == nil || !manager.Enabled {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.SessionID != oldID {
			return "", ErrStaleOperation
		}
		s.SessionID = newID
		s.invalidateLocked()
		return newID, nil
	}
	principal, err := contextstate.NewPrincipal(workspaceID, newID, subjectID)
	if err != nil {
		return "", err
	}
	var snapshot contextstate.Snapshot
	if manager != nil && manager.Enabled && store != nil {
		snapshot, err = ensureAndLoadContextStore(store, principal, binding)
		if err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SessionID != oldID {
		return "", ErrStaleOperation
	}
	s.SessionID = newID
	if manager != nil && manager.Enabled {
		s.contextPrincipal = principal
		s.contextHead = snapshot.Revision
	}
	s.invalidateLocked()
	return newID, nil
}
