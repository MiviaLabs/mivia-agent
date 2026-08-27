package chat

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// stubCalibrationStore implements both contextstate.Store (so it can be
// installed via Session.SetContextStore) and CalibrationSeeder (so
// RefreshCalibrationAfterModelSwitch's type assertion succeeds), with every
// Store method left unused - these tests never enable durable context, only
// exercise the calibration seam.
type stubCalibrationStore struct {
	stubCalibrationSeeder
}

func (stubCalibrationStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}
func (stubCalibrationStore) Commit(context.Context, contextstate.CommitRequest) error { return nil }
func (stubCalibrationStore) Advance(context.Context, contextstate.AdvanceRequest) error {
	return nil
}
func (stubCalibrationStore) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}

// TestRefreshCalibrationAfterModelSwitchReseedsForTheNewBinding pins the fix
// for a resume-time defect: enableSessionContext seeds calibration once,
// against the process's STARTUP binding, before a resumed session's Load
// publishes its real saved provider/model. SeedCalibration's own guard
// (already > 0) then refuses to correct the mistake on a second call, because
// it exists to protect a session's LIVE measurement from a stale seed - it
// cannot tell "already measured" apart from "seeded for the wrong model."
// RefreshCalibrationAfterModelSwitch must reset first so the guard does not
// block the correction.
func TestRefreshCalibrationAfterModelSwitchReseedsForTheNewBinding(t *testing.T) {
	sess := seedTestSession(t)
	// Simulate the stale pre-resume seed: some ratio, keyed to whatever
	// binding the session started with.
	sess.Calibration.Ratio = 3.0
	sess.Calibration.Samples = 1

	store := &stubCalibrationStore{stubCalibrationSeeder{ratio: 1.42, ok: true}}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}
	// The session's binding at refresh time is what Load would have just
	// published - seedTestSession's llmgateway/claude-sonnet-5 stands in for
	// "the resumed session's real saved model."
	sess.RefreshCalibrationAfterModelSwitch(context.Background())

	if store.calls != 1 {
		t.Fatalf("CalibrationSeed calls = %d, want exactly 1", store.calls)
	}
	if store.provider != "llmgateway" || store.model != "claude-sonnet-5" {
		t.Fatalf("seeded for %s/%s, want the session's post-Load binding", store.provider, store.model)
	}
	if sess.Calibration.Ratio != 1.42 {
		t.Fatalf("Ratio = %v, want the fresh seed 1.42 - the stale 3.0 must not survive", sess.Calibration.Ratio)
	}
	if sess.Calibration.Samples != 1 {
		t.Fatalf("Samples = %d, want exactly 1 so a live observation still dominates immediately", sess.Calibration.Samples)
	}
}

// TestRefreshCalibrationAfterModelSwitchMissTakesTheUncorrectedDefault covers
// the other half of the resume sequence the bug report actually hit: no
// durable observation exists yet for the newly-resumed binding (a session
// resumed onto a model this workspace has never sent a real request under).
// The stale pre-resume seed must still be discarded - keeping it would be
// worse than starting uncorrected, since it describes a different model's
// estimator bias.
func TestRefreshCalibrationAfterModelSwitchMissTakesTheUncorrectedDefault(t *testing.T) {
	sess := seedTestSession(t)
	sess.Calibration.Ratio = 3.0
	sess.Calibration.Samples = 1

	store := &stubCalibrationStore{stubCalibrationSeeder{ok: false}}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}
	sess.RefreshCalibrationAfterModelSwitch(context.Background())

	if sess.Calibration.Samples != 0 || sess.Calibration.Ratio != 0 {
		t.Fatalf("stale seed survived a miss: %+v, want the zero value", sess.Calibration)
	}
}

// TestRefreshCalibrationAfterModelSwitchToleratesNoSeeder covers a resumed
// session with no durable context store at all (or a store that does not
// implement CalibrationSeeder) - a legacy/non-context session, which never
// had a seed to begin with. The call must be a silent no-op, matching
// SeedCalibration's own "a missing seed must never be worse than the old
// unconditional 1.0" contract, not a panic on the failed type assertion.
func TestRefreshCalibrationAfterModelSwitchToleratesNoSeeder(t *testing.T) {
	sess := seedTestSession(t)
	sess.Calibration.Ratio = 1.5
	sess.Calibration.Samples = 4

	sess.RefreshCalibrationAfterModelSwitch(context.Background())

	if sess.Calibration.Ratio != 1.5 || sess.Calibration.Samples != 4 {
		t.Fatalf("no-store refresh changed calibration: %+v, want it untouched", sess.Calibration)
	}
}
