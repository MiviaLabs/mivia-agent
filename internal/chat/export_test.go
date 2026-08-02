package chat

// captureContextForTest exposes the per-turn context configuration so tests can
// drive finishAgentTurn's branches directly.
func (s *Session) captureContextForTest() contextTurnConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.captureContextLocked()
}
