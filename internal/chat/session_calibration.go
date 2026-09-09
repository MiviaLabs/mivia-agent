package chat

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
)

// adoptCalibration copies a finished turn's rolling token calibration back
// into the session so the next turn starts from it.
//
// Deliberately not fenced by the turn's operation token, unlike history: an
// estimate-vs-actual observation stays true even when the turn errored or its
// fence went stale, and discarding it would leave the heuristic uncorrected
// exactly on the long turns that drift most. Concurrent turns each start from
// the same seed, so the one with the most samples is the most informed; the
// count only ever grows on top of what the turn was seeded with.
func (s *Session) adoptCalibration(turnCalibration contextmgr.Calibration) {
	if turnCalibration.Samples == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnCalibration.Samples >= s.Calibration.Samples {
		s.Calibration = turnCalibration
	}
}

// CalibrationSeeder supplies the estimate-vs-actual correction ratio already
// observed for a (provider, model) binding. It is a one-method view of the
// durable usage ledger, declared here so internal/chat stays storage-agnostic
// - the composition root injects the concrete store.
type CalibrationSeeder interface {
	CalibrationSeed(ctx context.Context, workspaceID, provider, model string) (float64, bool, error)
}

// SeedCalibration primes the token-estimate correction from durable
// observations of this session's binding, so the FIRST request of a fresh
// process is planned with the correction the workspace already learned.
//
// The ratio was previously written to the usage ledger on every turn and
// never read back, so every process, session and resume began assuming the
// len(s)/4 estimate was exact. On payloads that are mostly code and JSON tool
// schemas it runs ~1.7x low, so the first request slipped past the compaction
// trigger and the next one repaid the whole error at once - the sequence that
// destroyed a real session's context.
//
// Seeding is a cold-start aid, never an override: a session that has already
// measured its own binding keeps that measurement, and Samples is set to 1
// (not the durable row count) so the first live observation outweighs the
// seed immediately and a stale ratio decays within a turn or two rather than
// pinning the estimate. Any failure leaves the session uncorrected, exactly
// as before this seam existed - a missing seed must never be worse than the
// old unconditional 1.0.
func (s *Session) SeedCalibration(ctx context.Context, seeder CalibrationSeeder, workspaceID string) {
	if seeder == nil {
		return
	}
	s.mu.RLock()
	already := s.Calibration.Samples
	provider, model := s.binding.ProviderName, s.binding.Model
	s.mu.RUnlock()
	if already > 0 || provider == "" || model == "" {
		return
	}
	ratio, ok, err := seeder.CalibrationSeed(ctx, workspaceID, provider, model)
	if err != nil || !ok || ratio <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Calibration.Samples > 0 {
		// A turn landed while the query was in flight; live evidence wins.
		return
	}
	s.Calibration.Ratio = ratio
	s.Calibration.Samples = 1
}

// RefreshCalibrationAfterModelSwitch discards session token-estimate calibration
// and re-seeds it from the durable usage ledger for the current (provider, model).
//
// This mirrors cliagents.RefreshSummarizerAfterModelSwitch at resumeChatSession
// and uiadapter/session_pool.go resume. Construction runs enableSessionContext's
// SeedCalibration with the startup model rather than the resumed model. Resumed
// sessions usually have different bindings, so the initial seed carries wrong
// bias or no observations (Samples=0, ratio 1.0). Uncorrected or wrongly corrected
// estimates can cause requests to slip past compaction limits.
//
// SeedCalibration cannot fix this directly because its Samples > 0 guard blocks
// updates. Because the guard protects wrong-model seeds rather than live measurements,
// this method resets Calibration to zero first, then runs SeedCalibration against
// the newly published binding from Load.
func (s *Session) RefreshCalibrationAfterModelSwitch(ctx context.Context) {
	seeder, ok := s.ContextStore().(CalibrationSeeder)
	if !ok {
		return
	}
	s.mu.Lock()
	s.Calibration = contextmgr.Calibration{}
	s.mu.Unlock()
	s.SeedCalibration(ctx, seeder, s.ContextPrincipal().WorkspaceID)
}
