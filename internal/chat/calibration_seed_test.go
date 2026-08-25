package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
)

type stubCalibrationSeeder struct {
	ratio    float64
	ok       bool
	err      error
	provider string
	model    string
	calls    int
}

func (s *stubCalibrationSeeder) CalibrationSeed(_ context.Context, _, provider, model string) (float64, bool, error) {
	s.calls++
	s.provider, s.model = provider, model
	return s.ratio, s.ok, s.err
}

func seedTestSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{ProviderName: "llmgateway", Model: "claude-sonnet-5"}, &fakeCompleter{out: "answer"})
}

// TestSeedCalibrationAppliesTheDurableRatio pins the fix for the deepest
// defect behind a real context-destroying compaction: the correction ratio
// was recorded to the durable usage ledger every turn and never read back, so
// every process started blind at 1.0 and planned its first request ~42% low.
func TestSeedCalibrationAppliesTheDurableRatio(t *testing.T) {
	sess := seedTestSession(t)
	if sess.Calibration.Samples != 0 {
		t.Fatalf("fresh session already has samples: %d", sess.Calibration.Samples)
	}
	seeder := &stubCalibrationSeeder{ratio: 1.73, ok: true}
	sess.SeedCalibration(context.Background(), seeder, "ws-1")

	if sess.Calibration.Ratio != 1.73 {
		t.Fatalf("Ratio = %v, want the durable 1.73", sess.Calibration.Ratio)
	}
	// Exactly one sample, NOT the durable row count: a live observation must
	// outweigh the seed immediately (adoptCalibration only adopts a turn whose
	// sample count is >= the session's), so a stale seed decays within a turn
	// instead of pinning the estimate.
	if sess.Calibration.Samples != 1 {
		t.Fatalf("Samples = %d, want exactly 1 so live observations dominate", sess.Calibration.Samples)
	}
	if seeder.provider != "llmgateway" || seeder.model != "claude-sonnet-5" {
		t.Fatalf("seeded for %s/%s, want the session's own binding", seeder.provider, seeder.model)
	}
}

// TestSeedCalibrationLeavesUncorrectedOnMiss: with no durable observation the
// session must keep the uncorrected default rather than adopt a fabricated
// ratio.
func TestSeedCalibrationLeavesUncorrectedOnMiss(t *testing.T) {
	for _, seeder := range []*stubCalibrationSeeder{
		{ok: false},
		{ratio: 1.9, ok: true, err: errors.New("store unavailable")},
	} {
		sess := seedTestSession(t)
		sess.SeedCalibration(context.Background(), seeder, "ws-1")
		if sess.Calibration.Samples != 0 || sess.Calibration.Ratio != 0 {
			t.Fatalf("session adopted a seed it should not have: %+v", sess.Calibration)
		}
	}
}

// TestSeedCalibrationNeverOverwritesLiveObservations: seeding is a cold-start
// aid only. A session that has already measured its own binding must keep
// that measurement - live evidence always beats history.
func TestSeedCalibrationNeverOverwritesLiveObservations(t *testing.T) {
	sess := seedTestSession(t)
	live := contextmgr.Calibration{}
	live.Update(1000, 1100)
	sess.adoptCalibration(live)

	sess.SeedCalibration(context.Background(), &stubCalibrationSeeder{ratio: 3.0, ok: true}, "ws-1")
	if sess.Calibration.Ratio != live.Ratio {
		t.Fatalf("Ratio = %v, want the live %v - a seed must not overwrite measurement", sess.Calibration.Ratio, live.Ratio)
	}
}

// TestSeedCalibrationToleratesNoSeeder: the seam is optional wiring, so a nil
// seeder must be a silent no-op rather than a panic.
func TestSeedCalibrationToleratesNoSeeder(t *testing.T) {
	sess := seedTestSession(t)
	sess.SeedCalibration(context.Background(), nil, "ws-1")
	if sess.Calibration.Samples != 0 {
		t.Fatalf("nil seeder changed calibration: %+v", sess.Calibration)
	}
}
