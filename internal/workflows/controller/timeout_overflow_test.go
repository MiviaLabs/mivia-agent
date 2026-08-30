package controller

import (
	"math"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestAdmissionDeadlineSaturatesInsteadOfOverflow pins saturation on an
// absurd limits.max_duration_seconds. The compiler rejects values outside
// [0, 86400], so this is defense in depth for a workflow that reaches the
// controller without that validation (a hand-built CompiledWorkflow, or a
// future compiler change). A bare multiply by time.Second overflows to a
// negative Duration, and the admission deadline then lands in the past -
// the run is expired before its first step.
func TestAdmissionDeadlineSaturatesInsteadOfOverflow(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctrl := newAdmissionFixture(t, repo)
	ctrl.Workflow.Limits.MaxDurationSeconds = int(math.MaxInt64)

	snap := ctrl.admissionSnapshot()
	if snap.DeadlineAt == nil {
		t.Fatalf("admissionSnapshot set no deadline for a positive max_duration_seconds")
	}
	if !snap.DeadlineAt.After(snap.StartedAt) {
		t.Fatalf("admission deadline %v is not after admission time %v: overflow armed an expired run", snap.DeadlineAt, snap.StartedAt)
	}
}
