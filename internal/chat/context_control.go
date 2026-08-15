package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
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
	input := prepareInputForContext(s.Messages, s.MaxContextTokens, s.MaxTokens, s.binding, cfg.principal, cfg.policy, cfg.worktree)
	input.Revision = cfg.revision
	return cfg.manager.PreparationManager, input, true
}

// SwapOnAgentEvent installs handler as the session's agent-event sink and
// returns the handler it replaced, so a caller that needs the events of one
// bounded operation (a manual compact runs outside any turn, where no turn
// callback is attached) can restore the previous sink afterwards. The swap
// takes the session mutex: emitContextCompaction reads the field under the
// read lock from the goroutine running the compaction, so a bare field
// assignment races it.
func (s *Session) SwapOnAgentEvent(handler func(agent.Event)) func(agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.OnAgentEvent
	s.OnAgentEvent = handler
	return previous
}

// Compact prepares and durably publishes the current conversation immediately.
// It is serialized with turn publication and never sends another provider call.
// focus is an optional caller-supplied bias string (e.g. `/compact <focus
// instructions>`, Claude Code parity) telling the summarizer what to
// prioritize; empty is the existing, unbiased behavior.
func (s *Session) Compact(ctx context.Context, focus string) error {
	_, err := s.compact(ctx, focus)
	return err
}

// CompactWithResult behaves exactly like Compact but also returns the
// contextmgr.Preparation the compaction produced, for callers (e.g. the
// `mivia compact` CLI command) that need to report before/after numbers.
func (s *Session) CompactWithResult(ctx context.Context, focus string) (contextmgr.Preparation, error) {
	return s.compact(ctx, focus)
}

// compactLoadSnapshot loads the durable snapshot for the bound session,
// validates it is current, and stamps the compact input's source range.
func (s *Session) compactLoadSnapshot(ctx context.Context, cfg contextTurnConfig, store contextstate.Store, input *contextmgr.PrepareInput) error {
	snapshot, err := loadBoundContextStore(ctx, store, cfg.principal, cfg.principal.SessionID, cfg.worktree)
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
	return nil
}

func (s *Session) compact(ctx context.Context, focus string) (contextmgr.Preparation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()

	s.mu.RLock()
	if s.activeTurns > 0 {
		s.mu.RUnlock()
		return contextmgr.Preparation{}, ErrStaleOperation
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
		return contextmgr.Preparation{}, fmt.Errorf("context compaction is not configured")
	}
	input := prepareInputForContext(messages, s.MaxContextTokens, s.MaxTokens, binding, cfg.principal, cfg.policy, cfg.worktree)
	input.Revision = cfg.revision
	input.Force = true
	if err := s.compactLoadSnapshot(ctx, cfg, store, &input); err != nil {
		return contextmgr.Preparation{}, err
	}
	preparation, err := cfg.manager.PreparationManager.Prepare(ctx, input)
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	if !preparation.Compacted {
		cfg.manager.PreparationManager.Discard(preparation)
		return contextmgr.Preparation{}, fmt.Errorf("context compaction made no reduction")
	}
	preparedMessages := cloneContextMessages(preparation.Messages)
	// Manual-compact summary: run the LLM summary before the durable commit
	// (the same order CommitPreparation uses), stamp the bounded metadata on
	// the candidate, and append the rendered message to the live history
	// after the state swap. Any summary failure keeps the structural compact
	// unchanged; a summary must never fail a manual compact.
	summaryMessage, haveSummary := summarizeManualCompact(ctx, cfg, input, messages, &preparation, focus)
	// The rendered summary joins the committed active context too, not only
	// the live history: the message is host-generated with no source event of
	// its own, so the checkpoint is its only durable carrier. Without it, a
	// restart before any later compaction loses the summary the model never
	// received (the compact delivered it to live memory only).
	active := preparedMessages
	if haveSummary {
		active = append(cloneContextMessages(preparedMessages), summaryMessage)
	}
	result := contextmgr.TurnResult{
		Active: active, TurnID: turnID, Outcome: contextmgr.OutcomeComplete,
	}
	if err == nil {
		err = cfg.manager.Commit(ctx, preparation, result)
	}
	cfg.manager.PreparationManager.Discard(preparation)
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	s.mu.Lock()
	if s.contextHead != cfg.revision || !s.contextEnabledLocked() {
		s.mu.Unlock()
		return contextmgr.Preparation{}, ErrStaleOperation
	}
	s.Messages = preparedMessages
	if haveSummary {
		s.Messages = append(s.Messages, summaryMessage)
	}
	s.contextHead = nextContextRevision(preparation, result)
	s.mu.Unlock()
	s.emitContextCompaction(preparation, turnID, haveSummary)
	return preparation, nil
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
	worktree := s.contextWorktree
	sessionDir := s.contextSessionDir
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
		snapshot, err = ensureAndLoadContextStore(store, principal, binding, worktree, sessionDir)
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
