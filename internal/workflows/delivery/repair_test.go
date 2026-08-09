package delivery

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// casRepairRunToDeliveryPending moves a pending run through the
// pending->running->delivery_pending chain under CAS, mirroring the ledger's
// status transitions for a workflow body that finished and entered delivery.
func casRepairRunToDeliveryPending(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string) {
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

// findRepairAttempt returns the latest wf-delivery attempt of a run, or nil.
func findRepairAttempt(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string) *workflowledger.StepAttempt {
	t.Helper()
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].StepID == DeliveryRepairStepID {
			recorded = &attempts[i]
		}
	}
	return recorded
}

// A delivery that fails for a reason an agent can repair must send the run
// back into the workflow, not stop it. The re-entry writes one wf-delivery
// attempt, completes it as failed with a route to the named repair step, and
// carries the failure text as a content-addressed ErrorRef the repair agent
// can read.
func TestReopenForRepairRoutesRunBackToTheNamedStep(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-route", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, repo, run.RunID)

	cause := errors.New("pre-commit: check_go_structure: 1 hard violation(s)")
	var stdout bytes.Buffer
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", cause, &stdout); err != nil {
		t.Fatalf("ReopenForRepair() error = %v, want nil", err)
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

	recorded := findRepairAttempt(t, ctx, repo, run.RunID)
	if recorded == nil {
		t.Fatal("no delivery attempt recorded; the failure must be in the run history")
	}
	if recorded.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("delivery attempt status = %q, want %q", recorded.Status, workflowledger.AttemptStatusFailed)
	}
	if recorded.ToStepID != "repair_preflight_structure" {
		t.Fatalf("delivery attempt route = %q, want %q", recorded.ToStepID, "repair_preflight_structure")
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

// A second delivery failure in the same run records a second attempt with the
// next attempt number rather than colliding with the first. A repair can fail
// more than once, and the attempt numbering must stay idempotent.
func TestReopenForRepairNumbersAttemptsIdempotently(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-twice", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", errors.New("first"), &stdout); err != nil {
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", errors.New("second"), &stdout); err != nil {
		t.Fatalf("second reopen: %v", err)
	}

	attempts, err := repo.ListStepAttempts(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	count := 0
	for _, a := range attempts {
		if a.StepID != DeliveryRepairStepID {
			continue
		}
		count++
		if prior, dup := seen[a.AttemptNo]; dup {
			t.Fatalf("attempt %d reused for both %q and %q; attempt numbers must be unique", a.AttemptNo, prior, a.AttemptID)
		}
		seen[a.AttemptNo] = a.AttemptID
	}
	if count != 2 {
		t.Fatalf("delivery attempts = %d, want 2", count)
	}
	if seen[1] == "" || seen[2] == "" {
		t.Fatalf("delivery attempt numbers = %v, want 1 and 2", seen)
	}
}

// The repair cycle is bounded. A rejection the named step cannot fix must not
// cycle until the step cap or the 24h run deadline is spent: the budget-exhausted
// run settles terminal to delivery_failed instead of waiting at delivery_pending
// forever.
func TestReopenForRepairBudgetExhaustionSettlesDeliveryFailed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-bound", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	for i := 0; i < MaxDeliveryRepairs; i++ {
		if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", errors.New("hook rejected"), &stdout); err != nil {
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", errors.New("hook rejected"), &stdout); err == nil {
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
	// No wf-delivery attempt is created for the exhausted settle: the budget is
	// spent, so this is a settle, not a re-entry.
	if recorded := findRepairAttempt(t, ctx, repo, run.RunID); recorded != nil && recorded.AttemptNo > MaxDeliveryRepairs {
		t.Fatalf("delivery attempt %d created beyond the budget of %d", recorded.AttemptNo, MaxDeliveryRepairs)
	}
}
