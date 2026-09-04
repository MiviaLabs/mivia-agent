package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func (s *Session) ContextStore() contextstate.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextStore
}
func (s *Session) ContextEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextEnabledLocked()
}
func (s *Session) ContextManager() *contextmgr.ContextManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.contextManager == nil {
		return nil
	}
	copyManager := *s.contextManager
	return &copyManager
}
func (s *Session) ContextPrincipal() contextstate.Principal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextPrincipal
}

// ContextWorktreeBinding returns the retained managed-worktree identity
// for this session - a copy under RLock; zero when the session is unbound.
// Read-only mirror of the Context* accessors above: chat stays the single
// holder of binding truth, callers only observe it (click-time fencing).
func (s *Session) ContextWorktreeBinding() contextstate.WorktreeInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextWorktree
}

// ContextWorktreeRoot returns the retained managed worktree root. The saved
// session directory may be a subdirectory, so callers that validate the
// physical worktree marker must use this root instead.
func (s *Session) ContextWorktreeRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextWorktreeRoot
}
