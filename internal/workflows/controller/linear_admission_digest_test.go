package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// A run admitted by an older binary must resume.
//
// The workflow digest is a hash of the marshalled definition struct, so it
// moves whenever those types gain a field, even when the workflow text does
// not change by one byte. Two fields were added in one day, and every run
// admitted before them became permanently unresumable with "already exists
// with different admission data". Work of many hours was lost twice.
//
// This drives ctrl.Run, the real entry point, NOT sameAdmission. A test of
// sameAdmission alone passes while resume still fails, because the defect is
// in what the caller PASSES, not in the comparison.
func TestResumeAcceptsADigestThisBinaryNoLongerComputes(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	const admittedByAnOlderBinary = "61a3dfcbd998841387f09d52dac98a5e91b979de95489a6144de02ffb87ee469"
	const invocationKey = "inv-key-from-the-agent-tool"

	ctrl := newAdmissionFixture(t, repo)
	admission := Admission{
		WorkflowDigest: admittedByAnOlderBinary,
		InvocationKey:  invocationKey,
	}
	if err := ctrl.SetAdmission(admission); err != nil {
		t.Fatal(err)
	}
	// Seed the run the way the older binary recorded it.
	seed := ctrl.admissionSnapshot()
	if seed.WorkflowDigest != admittedByAnOlderBinary {
		t.Fatalf("admissionSnapshot digest = %q, want the recorded one", seed.WorkflowDigest)
	}
	if err := repo.CreateRun(ctx, seed, ctrl.Snapshot); err != nil {
		t.Fatal(err)
	}

	if _, err := ctrl.StartNew(ctx); err != nil {
		t.Fatalf("resume of a run admitted by an older binary failed: %v", err)
	}
}

// A FRESH admission still uses this binary's digest, so the double-admission
// guard is unchanged. Without this, the fix above would silently disable the
// check for every new run.
func TestFreshAdmissionStillComparesThisBinarysDigest(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	seeded := newAdmissionFixture(t, repo)
	if err := seeded.SetAdmission(Admission{WorkflowDigest: "a-different-workflow-entirely"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, seeded.admissionSnapshot(), seeded.Snapshot); err != nil {
		t.Fatal(err)
	}

	fresh := newAdmissionFixture(t, repo)
	if err := fresh.SetAdmission(Admission{}); err != nil { // no recorded digest: fresh
		t.Fatal(err)
	}
	_, err := fresh.StartNew(ctx)
	if err == nil || !strings.Contains(err.Error(), "different admission data") {
		t.Fatalf("fresh admission error = %v, want the mismatch to still be refused", err)
	}
}

func newAdmissionFixture(t *testing.T, repo workflowledger.Repository) *LinearController {
	t.Helper()
	ctrl, err := NewLinearController(repo, &scriptedRunner{}, linearWorkflow(t), map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-admission-digest", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	return ctrl
}
