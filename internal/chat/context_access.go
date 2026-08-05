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
