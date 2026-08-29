package chat

import (
	"fmt"
	"sync"
	"testing"
)

// TestFencingFieldsUnderConcurrentTurnPressure closes the last of the three
// Tier-1 gaps: race_audit_test.go is real, race-detector-driven, and runs at
// high iteration counts, but it only exercises s.Messages/MessagesCopy/
// MessagesCount/UserTurns accessor safety - never the fence machinery itself
// (beginAgentTurn, PublishPendingAdmissionAtStepBoundary, and the
// operationEpoch/contextRevision/turnID/binding fields tokenCurrentLocked
// reads). Those fields' concurrency safety was previously "verified by
// reading the mutex discipline", not by an executable test.
//
// beginAgentTurn/context_integration.go documents (R2-1) that concurrent
// turns against one session are an explicitly supported case: it does not
// wait for a previous turn's done() before starting the next, it only
// increments activeTurns/turnID under s.mu and hands back an independent
// snapshot + release function. This test drives that pattern for real: N
// goroutines repeatedly begin turns without waiting on each other's done(),
// interleaved with N more goroutines repeatedly calling
// PublishPendingAdmissionAtStepBoundary (which itself takes s.mu and, when a
// stage is staged and pending, bumps the operation fence and re-captures
// s.liveTurnToken).
//
// This test's entire value is proving the ACCESS PATTERN is race-clean at
// the Go-memory-model level (go test -race reports no data race, and no
// goroutine panics) - not the semantic outcome of who "wins" any one
// interleaving, which the fencing design's own tests already cover with
// hand-sequenced and real-goroutine cases elsewhere in this package.
func TestFencingFieldsUnderConcurrentTurnPressure(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "ok"})
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)

	const turnGoroutines = 4
	const publishGoroutines = 4
	const iterations = 500

	var wg sync.WaitGroup

	wg.Add(turnGoroutines)
	for g := 0; g < turnGoroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				snapshot, done, err := sess.beginAgentTurn(fmt.Sprintf("g%d-turn%d", id, i), nil)
				if err != nil {
					// A concurrent switch/load/clear may legitimately refuse a
					// turn start; that is expected contention, not a bug.
					continue
				}
				// Stage a name against this turn every few iterations so
				// PublishPendingAdmissionAtStepBoundary has real, contended
				// work to do (pendingAdmission, agentSurfaceGeneration,
				// liveTurnToken) rather than only taking an early-return path.
				if i%3 == 0 {
					_, _ = sess.StageToolAdmission([]string{"grep"}, snapshot.myTurn)
				}
				done()
			}
		}(g)
	}

	wg.Add(publishGoroutines)
	for g := 0; g < publishGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = sess.PublishPendingAdmissionAtStepBoundary()
			}
		}()
	}

	wg.Wait()
}
