package chat

import "os"

func init() {
	// Safety net: if a test panics and leaves syncFile set to a tracking
	// wrapper, restore the real implementation so subsequent tests are not
	// affected.
	syncFile = (*os.File).Sync
}

// captureContextForTest exposes the per-turn context configuration so tests can
// drive finishAgentTurn's branches directly.
func (s *Session) captureContextForTest() contextTurnConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.captureContextLocked()
}
