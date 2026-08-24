package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (s *Session) resetSystem() error {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextWorktree := s.contextWorktree
	contextExpected := s.contextHead
	contextBinding := captureBindingRevision(s.binding)
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	s.mu.Unlock()
	// Advance the durable head BEFORE mutating in-memory state, so that a
	// failure leaves the conversation intact and the user can retry.  This is
	// the INV-AG-35 guarantee: a refused commit must never destroy state
	// the user already has, and /clear is a commit from the user's
	// perspective.
	if contextEnabled {
		if err := s.advanceContextHead(contextStore, contextPrincipal, contextWorktree, contextExpected, contextBinding, contextBinding, "clear", true); err != nil {
			return err
		}
	}
	s.mu.Lock()
	// Replacing history wholesale invalidates any turn already in flight: bump
	// the generation so its writeback fails the myTurn == s.turnID check.
	// Without this, /clear is silently undone by the running turn and the
	// purged conversation is restored - then persisted by SaveAfterTurn.
	s.invalidateLocked()
	s.turnID++
	s.Messages = nil
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: s.SystemPrompt,
		})
	}
	// /clear keeps the core-memory context message alongside the system
	// prompt: both are session surface, not conversation history.
	if s.memoryContext != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleUser,
			Content: s.memoryContext,
			Name:    MemoryContextMessageName,
		})
	}
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	s.mu.Unlock()
	if contextEnabled {
		s.autoSaveContextSession()
	}
	return nil
}

// Clear drops conversation history but keeps the system prompt.
func (s *Session) Clear() error {
	return s.resetSystem()
}
