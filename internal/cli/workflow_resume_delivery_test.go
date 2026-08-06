package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// casRunTo moves the fixture run through pending->running->delivery_pending
// under CAS on the versions observed from GetRun, mirroring the ledger's
// status transitions for a workflow body that finished and entered delivery.
func casRunToDeliveryPending(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string) {
	t.Helper()
	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusDeliveryPending,
	} {
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, status, nil); err != nil {
			t.Fatalf("CompareAndSetRunStatus(%q, %q): %v", runID, status, err)
		}
	}
}

// routeAttemptToSuccess records a completed attempt that routed the run to the
// reserved terminal step "success" without a run status CAS — the exact shape
// that an older classification would have settled to succeeded, skipping
// delivery for a delivery_pending run.
func routeAttemptToSuccess(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID, stepID string) {
	t.Helper()
	attempt := workflowledger.StepAttempt{
		AttemptID: "att-" + stepID, RunID: runID, StepID: stepID, AttemptNo: 1,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	outcome := workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "success", MatchDigest: "match",
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, outcome); err != nil {
		t.Fatal(err)
	}
}

// TestResumeRefusesDeliveryPending: a delivery_pending run (whose derived
// active step routed to the reserved "success" terminal) must be refused by
// executeWorkflowResume BEFORE any terminal reconciliation — and left
// completely untouched: still delivery_pending, same version. Delivery is a
// separate host-owned step; resume must never CAS it (an older classification
// could settle delivery_pending->succeeded and skip delivery).
func TestResumeRefusesDeliveryPending(t *testing.T) {
	root, configPath, repo, run := newResumeFailureFixture(t)
	ctx := context.Background()
	casRunToDeliveryPending(t, ctx, repo, run.RunID)
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	before, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("fixture status = %q, want %q", before.Status, workflowledger.RunStatusDeliveryPending)
	}

	originalOpen := workflowResumeOpenStore
	t.Cleanup(func() { workflowResumeOpenStore = originalOpen })
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}

	err = executeWorkflowResume(run.RunID, root, configPath, true, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("executeWorkflowResume() error = %v, want a 'waiting for delivery' refusal", err)
	}

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (delivery_pending must not be settled)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version != before.Version {
		t.Fatalf("run version = %d, want %d (delivery_pending must not be CASed)", after.Version, before.Version)
	}
}

// TestReconcileWorkflowTerminalSkipsDeliveryPendingCAS: the reconcile step
// reports a settled delivery_pending run as terminal WITHOUT error and WITHOUT
// touching it. CASing delivery_pending->delivery_pending is an invalid
// transition, so skipping the CAS is both required and the observable contract.
func TestReconcileWorkflowTerminalSkipsDeliveryPendingCAS(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-delivery-pending-cas", Status: workflowledger.RunStatusPending, ActiveStepID: "two",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRunToDeliveryPending(t, ctx, repo, run.RunID)
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "two")
	before, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v, want nil (no delivery_pending->delivery_pending CAS)", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a settled delivery_pending run")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (must not settle to succeeded)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version != before.Version {
		t.Fatalf("run version = %d, want %d (no CAS on a settled delivery_pending run)", after.Version, before.Version)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("reconcile output = %q, want status=delivery_pending", stdout.String())
	}
}

// TestReconcileWorkflowTerminalStillSettlesDerivedSuccess: regression guard
// against over-broadening the skip — a RUNNING run whose attempt routed to the
// reserved terminal step "success" (no status CAS yet) must still be settled
// to succeeded, bumping the version. Only the equal-status case (delivery
// pending settled) may skip the CAS.
func TestReconcileWorkflowTerminalStillSettlesDerivedSuccess(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-derived-success-still", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a run routed to success")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want %q (derived success must still CAS)", after.Status, workflowledger.RunStatusSucceeded)
	}
	if after.Version <= stored.Version {
		t.Fatalf("run version = %d, want > %d (derived success must CAS and bump)", after.Version, stored.Version)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("reconcile output = %q, want status=succeeded", stdout.String())
	}
}

// TestReconcileWorkflowTerminalDeliveryActiveSettlesDeliveryPending: a RUNNING
// run whose attempt durably routed to "success" under an active delivery
// policy must settle to delivery_pending (never succeeded): the durable route
// write happened but the delivery_pending CAS was lost to a crash. Delivery
// must not be skipped by the resume path.
func TestReconcileWorkflowTerminalDeliveryActiveSettlesDeliveryPending(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-derived-success-delivery", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, true, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a run routed to success")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (delivery policy must settle to delivery_pending, never succeeded)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version <= stored.Version {
		t.Fatalf("run version = %d, want > %d (the settle CAS must bump the version once)", after.Version, stored.Version)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("reconcile output = %q, want status=delivery_pending", stdout.String())
	}
	// A follow-up reconcile must be a no-op: settled status equals the plan's.
	before := after
	var again bytes.Buffer
	terminal, err = reconcileWorkflowTerminal(ctx, repo, run.RunID, true, &again)
	if err != nil {
		t.Fatalf("second reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("second reconcileWorkflowTerminal() = false, want true")
	}
	after, err = repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending || after.Version != before.Version {
		t.Fatalf("second reconcile changed the run: status=%q version=%d, want delivery_pending/%d (no-op)", after.Status, after.Version, before.Version)
	}
}
