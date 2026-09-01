package chat

// captureContextForTest exposes the per-turn context configuration so tests can
// drive finishAgentTurn's branches directly.
func (s *Session) captureContextForTest() contextTurnConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.captureContextLocked()
}

// prefixIdentityCaptureCountForTest exposes the internal capture counter so
// the INV-68-8 test can prove per-turn save paths never recapture the prefix
// identity.
func (s *Session) prefixIdentityCaptureCountForTest() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prefixIdentityCaptures
}
