package chat

import (
	"context"
	"testing"
)

// inflightTurnSeeder is a seeder that lands a live observation on the
// session while its own query is still in flight. The seeder call runs
// with no session lock held - that is the whole window this covers.
type inflightTurnSeeder struct {
	session   *Session
	liveRatio float64
	seedRatio float64
}

func (s *inflightTurnSeeder) CalibrationSeed(_ context.Context, _, _, _ string) (float64, bool, error) {
	s.session.mu.Lock()
	s.session.Calibration.Ratio = s.liveRatio
	s.session.Calibration.Samples = 4
	s.session.mu.Unlock()
	return s.seedRatio, true, nil
}

// TestSeedCalibrationYieldsToATurnThatLandedMidQuery pins the re-check
// after the durable read returns. SeedCalibration drops the session lock
// while it queries the ledger, so a turn can land a real measurement in
// that window. Overwriting it with the historical seed would replace live
// evidence with history and re-introduce the mis-planning the seed exists
// to prevent - and the first Samples check, made before the query, cannot
// see it.
func TestSeedCalibrationYieldsToATurnThatLandedMidQuery(t *testing.T) {
	sess := seedTestSession(t)
	seeder := &inflightTurnSeeder{session: sess, liveRatio: 1.21, seedRatio: 1.73}

	sess.SeedCalibration(context.Background(), seeder, "ws-1")

	if sess.Calibration.Ratio != seeder.liveRatio {
		t.Fatalf("Ratio = %v; the seed overwrote the live measurement %v", sess.Calibration.Ratio, seeder.liveRatio)
	}
	if sess.Calibration.Samples != 4 {
		t.Fatalf("Samples = %d; want the live turn's 4, not the seed's 1", sess.Calibration.Samples)
	}
}
