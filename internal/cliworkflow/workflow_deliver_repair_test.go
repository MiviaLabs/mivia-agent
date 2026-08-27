package cliworkflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// A delivery that fails for a reason an agent can repair must send the run
// back into the workflow, not stop it.
//
// Delivery runs after the success terminal, outside the step graph. A commit
// hook that rejects the change therefore stopped a run that had passed every
// gate, with no route back. This is the end to end shape of the recovery: the
// run returns to the named step, the failure text travels with it, and the run
// is running again so a resume continues the work.
func TestFailedDeliveryReturnsTheRunToItsRepairStep(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run := workflowledger.RunSnapshot{
		RunID: "wfr-deliver-repair", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRunToDeliveryPending(t, ctx, repo, run.RunID)

	cause := errors.New("pre-commit: check_go_structure: 1 hard violation(s)")
	var stdout bytes.Buffer
	if err := delivery.ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", delivery.MaxDeliveryRepairs, cause, &stdout); err != nil {
		t.Fatalf("delivery.ReopenForRepair() error = %v, want nil", err)
	}

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want %q: a repairable delivery failure must not stop the run",
			after.Status, workflowledger.RunStatusRunning)
	}
	// The ledger derives the active step from the last attempt's route, so
	// this is what a resume will dispatch.
	if after.ActiveStepID != "repair_preflight_structure" {
		t.Fatalf("active step = %q, want %q", after.ActiveStepID, "repair_preflight_structure")
	}

	attempts, err := repo.ListStepAttempts(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].StepID == delivery.DeliveryRepairStepID {
			recorded = &attempts[i]
		}
	}
	if recorded == nil {
		t.Fatal("no delivery attempt recorded; the failure must be in the run history")
	}
	if recorded.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("delivery attempt status = %q, want %q", recorded.Status, workflowledger.AttemptStatusFailed)
	}
	// The repair agent must be able to read WHY delivery failed.
	if recorded.ErrorRef == "" {
		t.Fatal("delivery attempt has no ErrorRef; the repair agent would have no evidence")
	}
	body, err := repo.LoadContent(ctx, recorded.ErrorRef)
	if err != nil {
		t.Fatalf("load failure evidence: %v", err)
	}
	if !strings.Contains(string(body), "check_go_structure") {
		t.Fatalf("failure evidence = %q, want the delivery failure text", body)
	}
	if !strings.Contains(stdout.String(), "repairing at step") {
		t.Fatalf("output = %q, want it to name the repair", stdout.String())
	}
}

// A second delivery failure in the same run records a second attempt rather
// than colliding with the first. A repair can fail more than once.
func TestRepeatedDeliveryFailuresEachRecordAnAttempt(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run := workflowledger.RunSnapshot{
		RunID: "wfr-deliver-repair-twice", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	if err := delivery.ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", delivery.MaxDeliveryRepairs, errors.New("first"), &stdout); err != nil {
		t.Fatalf("first reopen: %v", err)
	}
	// The run is running again after the first repair; it reaches delivery
	// once more the same way the controller settles a success terminal.
	backToPending, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, backToPending.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	if err := delivery.ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", delivery.MaxDeliveryRepairs, errors.New("second"), &stdout); err != nil {
		t.Fatalf("second reopen: %v", err)
	}

	attempts, err2 := repo.ListStepAttempts(ctx, run.RunID)
	if err2 != nil {
		t.Fatal(err2)
	}
	count := 0
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("delivery attempts = %d, want 2", count)
	}
}

// The repair cycle is bounded. A rejection the named step cannot fix must not
// cycle until the step cap or the 24h run deadline is spent.
func TestDeliveryRepairIsBounded(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-deliver-repair-bound", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	for i := 0; i < delivery.MaxDeliveryRepairs; i++ {
		if err := delivery.ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", delivery.MaxDeliveryRepairs, errors.New("hook rejected"), &stdout); err != nil {
			t.Fatalf("repair %d refused: %v", i+1, err)
		}
		back, err := repo.GetRun(ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, run.RunID, back.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := delivery.ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", delivery.MaxDeliveryRepairs, errors.New("hook rejected"), &stdout); err == nil {
		t.Fatal("the repair budget is spent; a further re-entry must fail")
	}
	// The budget-exhausted run must settle terminal instead of waiting at
	// delivery_pending forever: resume and cancel both refuse delivery_pending,
	// and cleanup would remove the worktree without settling, so an unsettled
	// run looks waiting but can never be delivered.
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want %q: a budget-exhausted run must settle as delivery_failed, not wait at delivery_pending forever",
			after.Status, workflowledger.RunStatusDeliveryFailed)
	}
	if !workflowledger.IsTerminalRunStatus(after.Status) {
		t.Fatalf("run status = %q is not terminal; the budget-exhausted run must be settled", after.Status)
	}
}
