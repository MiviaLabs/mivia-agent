package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// failingAttemptWriter wraps a repository and fails every attempt-outcome
// write, simulating a crash or storage fault at the exact moment the delivery
// re-entry records its attempt.
type failingAttemptWriter struct {
	workflowledger.Repository
	err error
}

func (f *failingAttemptWriter) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	return f.err
}

func (f *failingAttemptWriter) RecordStepAttemptOutcome(ctx context.Context, attempt workflowledger.StepAttempt, outcome workflowledger.AttemptOutcome) error {
	return f.err
}

// TestReopenForRepairAtomicReentryWrite pins the atomic re-entry contract: a
// failure while recording the delivery re-entry must not leave a Running
// wf-delivery attempt behind. Before the fix ReopenForRepair wrote the
// Running attempt (CreateStepAttempt) and then completed it in a SEPARATE
// write, so a failure between the two left a Running undeclared-step attempt
// that made the run permanently unresumable; after the fix the single-write
// API fails atomically and no attempt exists at all.
func TestReopenForRepairAtomicReentryWrite(t *testing.T) {
	ctx := context.Background()
	base := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = base.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-atomic", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := base.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, base, run.RunID)

	sentinel := errors.New("attempt write failed")
	repo := &failingAttemptWriter{Repository: base, err: sentinel}

	var stdout bytes.Buffer
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, errors.New("hook rejected"), &stdout); err == nil {
		t.Fatal("ReopenForRepair() must surface the failed attempt write")
	}

	// The failed re-entry must not leave any wf-delivery attempt in a
	// non-terminal state: the attempt and its terminal outcome are one write,
	// so a failing write leaves no attempt at all.
	attempts, err := base.ListStepAttempts(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID == DeliveryRepairStepID && !workflowledger.IsTerminalAttemptStatus(a.Status) {
			t.Fatalf("failed re-entry left a non-terminal wf-delivery attempt %s (status %q)", a.AttemptID, a.Status)
		}
	}
	// A resume must find nothing in flight: the run is running with no
	// attempts, so the CLI join loop has nothing to hard-fail on.
	plan, err := workflowledger.PlanResume(ctx, base, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Fatalf("PlanResume.AttemptsInFlight = %d, want 0 after a failed re-entry write", len(plan.AttemptsInFlight))
	}
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, cause, &stdout); err != nil {
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, errors.New("first"), &stdout); err != nil {
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, errors.New("second"), &stdout); err != nil {
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
	// The real engine records each failed delivery as a wf-delivery attempt
	// BEFORE ReopenForRepair runs (the live loop shows attempts 1..k carrying
	// the refusal errors), so the fixture seeds the first failed delivery and
	// the reopens below create attempts 2..MaxDeliveryRepairs. After
	// MaxDeliveryRepairs failed deliveries the budget is spent.
	seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 1, "", "hook rejected")
	for i := 0; i < MaxDeliveryRepairs-1; i++ {
		if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, errors.New("hook rejected"), &stdout); err != nil {
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
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, errors.New("hook rejected"), &stdout); err == nil {
		t.Fatal("the repair budget is spent; a further re-entry must fail")
	} else if want := fmt.Sprintf("delivery failed %d times", MaxDeliveryRepairs); !strings.Contains(err.Error(), want) {
		// The exhaustion diagnostic must report the ACTUAL delivery-failure
		// count: the seeded failure plus MaxDeliveryRepairs-1 re-entry
		// failures, i.e. MaxDeliveryRepairs failed deliveries. The message
		// must print next-1 (the highest recorded attempt number), never
		// next: the failing attempt is recorded before ReopenForRepair runs,
		// so the attempt it would create next is not a failure yet.
		t.Fatalf("exhaustion error = %q, want it to report %q", err.Error(), want)
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

// The repair-cycle budget is configurable per workflow via delivery.max_repairs
// (snapshotted into Policy.MaxRepairs and passed through ReopenForRepair). A
// budget of 1 allows exactly one re-entry; the next failure settles terminal.
func TestReopenForRepairHonorsConfiguredBudget(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-budget-one", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", 1, errors.New("hook rejected"), &stdout); err != nil {
		t.Fatalf("first re-entry within budget 1: %v", err)
	}
	back, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, back.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", 1, errors.New("hook rejected"), &stdout); err == nil {
		t.Fatal("budget 1 is spent after one re-entry; a further re-entry must fail")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want %q", after.Status, workflowledger.RunStatusDeliveryFailed)
	}
}

// RepairHint renders the harness guidance the repair agent sees via
// delivery.failure: a class-specific "what to repair" lead plus the raw
// rejection text. The lead must never name a repository's tests, files,
// tools, or gate names - it stays project- and language-agnostic.
func TestRepairHintClassifies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"gate rejection", errors.New("pre-push: check_go_structure: 1 hard violation(s)"),
			[]string{"the delivery gate rejected the change", "rejection output:", "check_go_structure"}},
		{"PR metadata", &PRMetadataError{Reason: "title too long"},
			[]string{"pr_title and pr_summary", "title too long"}},
		{"diff too large", &DiffSizeError{Reason: "delivery: chunk diff size 500 exceeds hard limit 400"},
			[]string{"delivered diff is too large", "automatic file split", "hard limit"}},
		{"permanent refusal", &RefusalError{Reason: "origin remote changed since admission"},
			[]string{"permanently refused publication", "origin remote changed"}},
		{"nil cause", nil, []string{"without a recorded cause"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RepairHint(tc.err)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("RepairHint(%v) = %q, missing %q", tc.err, got, want)
				}
			}
		})
	}
}

// The stored delivery-failure evidence is the harness hint, not just the raw
// error: the repair agent reads what to repair (and the commit guidance)
// through the wf-delivery attempt's ErrorRef.
func TestReopenForRepairStoresHarnessHint(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-repair-hint-evidence", Status: workflowledger.RunStatusPending, ActiveStepID: "preflight_structure",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRepairRunToDeliveryPending(t, ctx, repo, run.RunID)

	var stdout bytes.Buffer
	cause := errors.New("pre-push: verify gate rejected the change")
	if err := ReopenForRepair(ctx, repo, run.RunID, "repair_preflight_structure", MaxDeliveryRepairs, cause, &stdout); err != nil {
		t.Fatalf("ReopenForRepair() error = %v, want nil", err)
	}
	recorded := findRepairAttempt(t, ctx, repo, run.RunID)
	if recorded == nil || recorded.ErrorRef == "" {
		t.Fatal("no delivery attempt with an ErrorRef; the repair agent would have no evidence")
	}
	body, err := repo.LoadContent(ctx, recorded.ErrorRef)
	if err != nil {
		t.Fatalf("load failure evidence: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "what to repair") && !strings.Contains(text, "the delivery gate rejected the change") {
		t.Fatalf("stored evidence = %q, want the harness hint lead", text)
	}
	if !strings.Contains(text, cause.Error()) {
		t.Fatalf("stored evidence = %q, want the raw rejection text", text)
	}
}

// TestRepairTarget pins the single delivery-repair classifier both the CLI and
// the local engine route through: diff-size rejections go to
// OnDiffSizeFailure (falling back to OnFailure), PR-metadata rejections to
// OnPRMetadataFailure (falling back to OnFailure), and every other repairable
// rejection to OnFailure. An empty policy on_failure leaves the class unrouted
// (the run holds for a person).
func TestRepairTarget(t *testing.T) {
	base := Policy{OnFailure: "repair_generic", OnPRMetadataFailure: "repair_meta", OnDiffSizeFailure: "repair_size"}
	tests := []struct {
		name string
		pol  Policy
		err  error
		want string
	}{
		{"diff-size routes to the diff-size repair step", base, &DiffSizeError{Reason: "too big"}, "repair_size"},
		{"PR metadata routes to the metadata repair step", base, &PRMetadataError{Reason: "bad title"}, "repair_meta"},
		{"generic rejection routes to on_failure", base, errors.New("hook rejected"), "repair_generic"},
		{"nil error routes to on_failure", base, nil, "repair_generic"},
		{"diff-size falls back to on_failure", Policy{OnFailure: "repair_generic"}, &DiffSizeError{Reason: "too big"}, "repair_generic"},
		{"PR metadata falls back to on_failure", Policy{OnFailure: "repair_generic"}, &PRMetadataError{Reason: "bad title"}, "repair_generic"},
		{"wrapped diff-size is classified", base, fmt.Errorf("deliver: %w", &DiffSizeError{Reason: "too big"}), "repair_size"},
		{"no repair route stays unrouted", Policy{}, errors.New("hook rejected"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RepairTarget(tc.err, tc.pol); got != tc.want {
				t.Fatalf("RepairTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}
