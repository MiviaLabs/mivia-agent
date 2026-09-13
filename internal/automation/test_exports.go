package automation

import "time"

// test_exports.go exposes executor internals a cross-package test needs to
// assert on the Service a host actually built, rather than on the helper
// that host was supposed to call. The distinction is not academic: a test
// that re-derived the expected deadline from the same helper passed with
// the production wiring deleted.

// TurnTimeoutForTest reports the per-step deadline this Service applies -
// Config.TurnTimeout when a host set one, otherwise defaultTurnTimeout.
func (s *Service) TurnTimeoutForTest() time.Duration { return s.turnTimeout() }
