package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testNow is the fixed clock used by every test in this file. The repository's
// clock is pinned so all timestamps are deterministic.
var testNow = time.Date(2025, time.January, 15, 10, 30, 0, 0, time.UTC)

// newTestRepo builds an in-memory repository with a fixed clock.
func newTestRepo(t *testing.T) *StorageRepository {
	t.Helper()
	repo := NewMemoryRepository()
	repo.SetTimeSource(func() time.Time { return testNow })
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// createRun admits a fresh pending run whose initial step is "plan".
func createRun(t *testing.T, repo Repository, runID string) {
	t.Helper()
	snap := RunSnapshot{
		RunID:          runID,
		WorkflowName:   "feature-delivery",
		WorkflowDigest: "test-digest",
		Status:         RunStatusPending,
		ActiveStepID:   "plan",
		StartedAt:      testNow,
	}
	snapshotJSON, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := repo.CreateRun(context.Background(), snap, snapshotJSON); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}
}

// casRunStatus moves the run to status under CAS on the version observed from
// GetRun. finishedAt is passed for terminal statuses.
func casRunStatus(t *testing.T, repo Repository, runID string, status RunStatus) {
	t.Helper()
	snap, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun(%q): %v", runID, err)
	}
	var finishedAt *time.Time
	if IsTerminalRunStatus(status) {
		finishedAt = &testNow
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, snap.Version, status, finishedAt); err != nil {
		t.Fatalf("CompareAndSetRunStatus(%q, %q): %v", runID, status, err)
	}
}

// createAttempt records a fresh numbered attempt for a step.
func createAttempt(t *testing.T, repo Repository, runID, stepID string, attemptNo int, coordinatorRunID, taskID string) StepAttempt {
	t.Helper()
	attempt := StepAttempt{
		AttemptID:        fmt.Sprintf("att-%s-%d", stepID, attemptNo),
		RunID:            runID,
		StepID:           stepID,
		AttemptNo:        attemptNo,
		Status:           AttemptStatusPending,
		CoordinatorRunID: coordinatorRunID,
		TaskID:           taskID,
		StartedAt:        testNow,
	}
	if err := repo.CreateStepAttempt(context.Background(), attempt); err != nil {
		t.Fatalf("CreateStepAttempt(%q): %v", attempt.AttemptID, err)
	}
	return attempt
}

// completeAttempt completes an attempt under CAS on the version observed from
// GetStepAttempt.
func completeAttempt(t *testing.T, repo Repository, runID string, attempt StepAttempt, outcome AttemptOutcome) {
	t.Helper()
	stored, err := repo.GetStepAttempt(context.Background(), runID, attempt.AttemptID)
	if err != nil {
		t.Fatalf("GetStepAttempt(%q, %q): %v", runID, attempt.AttemptID, err)
	}
	if err := repo.CompleteStepAttempt(context.Background(), runID, attempt.AttemptID, stored.Version, outcome); err != nil {
		t.Fatalf("CompleteStepAttempt(%q, %q): %v", runID, attempt.AttemptID, err)
	}
}

// assertAttemptEquals checks the fields that define a recorded attempt's
// identity and JOIN evidence without comparing bookkeeping (Version,
// FinishedAt) that the repository owns.
func assertAttemptEquals(t *testing.T, got, want StepAttempt) {
	t.Helper()
	if got.AttemptID != want.AttemptID {
		t.Errorf("AttemptID = %q, want %q", got.AttemptID, want.AttemptID)
	}
	if got.RunID != want.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, want.RunID)
	}
	if got.StepID != want.StepID {
		t.Errorf("StepID = %q, want %q", got.StepID, want.StepID)
	}
	if got.AttemptNo != want.AttemptNo {
		t.Errorf("AttemptNo = %d, want %d", got.AttemptNo, want.AttemptNo)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.CoordinatorRunID != want.CoordinatorRunID {
		t.Errorf("CoordinatorRunID = %q, want %q (JOIN evidence)", got.CoordinatorRunID, want.CoordinatorRunID)
	}
	if got.TaskID != want.TaskID {
		t.Errorf("TaskID = %q, want %q (JOIN evidence)", got.TaskID, want.TaskID)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
}

// TestPlanResumeTerminalRun: a run whose status CAS reached a terminal status
// is Terminal, with TerminalStatus set and nothing in flight.
func TestPlanResumeTerminalRun(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-terminal-1"
	createRun(t, repo, runID)
	casRunStatus(t, repo, runID, RunStatusRunning)
	casRunStatus(t, repo, runID, RunStatusSucceeded)

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if !plan.Terminal {
		t.Errorf("Terminal = false, want true (run status %q)", plan.Run.Status)
	}
	if plan.TerminalStatus != RunStatusSucceeded {
		t.Errorf("TerminalStatus = %q, want %q", plan.TerminalStatus, RunStatusSucceeded)
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Errorf("AttemptsInFlight = %v, want empty", plan.AttemptsInFlight)
	}
}

// TestPlanResumeInFlightAttempt: a pending recorded attempt is returned in
// AttemptsInFlight with its CoordinatorRunID/TaskID preserved (the JOIN
// evidence), and NextAttemptNo advances.
func TestPlanResumeInFlightAttempt(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-inflight-1"
	createRun(t, repo, runID)
	casRunStatus(t, repo, runID, RunStatusRunning)

	want := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if plan.Terminal {
		t.Errorf("Terminal = true, want false")
	}
	if len(plan.AttemptsInFlight) != 1 {
		t.Fatalf("len(AttemptsInFlight) = %d, want 1", len(plan.AttemptsInFlight))
	}
	assertAttemptEquals(t, plan.AttemptsInFlight[0], want)
	if plan.NextAttemptNo != 2 {
		t.Errorf("NextAttemptNo = %d, want 2", plan.NextAttemptNo)
	}
}

// TestPlanResumeInterruptedThenResume: an attempt completed as interrupted is
// not in flight and the next attempt number resumes at 2 for the same step.
func TestPlanResumeInterruptedThenResume(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-interrupted-1"
	createRun(t, repo, runID)
	attempt := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
	completeAttempt(t, repo, runID, attempt, AttemptOutcome{Status: AttemptStatusInterrupted})

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Errorf("AttemptsInFlight = %v, want empty", plan.AttemptsInFlight)
	}
	if plan.NextAttemptNo != 2 {
		t.Errorf("NextAttemptNo = %d, want 2", plan.NextAttemptNo)
	}
}

// TestPlanResumeCompletedWithRoute: a succeeded attempt routed to "implement"
// leaves nothing in flight, resets NextAttemptNo to 1 for the fresh step, and
// derives the active step from the completion's to_step_id.
func TestPlanResumeCompletedWithRoute(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-routed-1"
	createRun(t, repo, runID)
	attempt := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
	completeAttempt(t, repo, runID, attempt, AttemptOutcome{
		Status:   AttemptStatusSucceeded,
		ToStepID: "implement",
	})

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Errorf("AttemptsInFlight = %v, want empty", plan.AttemptsInFlight)
	}
	if plan.NextAttemptNo != 1 {
		t.Errorf("NextAttemptNo = %d, want 1 (fresh step %q)", plan.NextAttemptNo, "implement")
	}
	if plan.Run.ActiveStepID != "implement" {
		t.Errorf("Run.ActiveStepID = %q, want %q", plan.Run.ActiveStepID, "implement")
	}
	if plan.Terminal {
		t.Errorf("Terminal = true, want false")
	}
}

// TestPlanResumeRouteToTerminalWithoutStatusCAS: routing a completed attempt to
// a reserved terminal step makes the run Terminal even though no run status CAS
// was recorded, with the matching TerminalStatus and a non-empty Reason.
func TestPlanResumeRouteToTerminalWithoutStatusCAS(t *testing.T) {
	cases := []struct {
		name   string
		toStep string
		want   RunStatus
	}{
		{name: "success", toStep: "success", want: RunStatusSucceeded},
		{name: "failure", toStep: "failure", want: RunStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			runID := "wfr-terminal-route-" + tc.name
			createRun(t, repo, runID)
			attempt := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
			completeAttempt(t, repo, runID, attempt, AttemptOutcome{
				Status:   AttemptStatusSucceeded,
				ToStepID: tc.toStep,
			})

			plan, err := PlanResume(context.Background(), repo, runID)
			if err != nil {
				t.Fatalf("PlanResume(%q): %v", runID, err)
			}
			if !plan.Terminal {
				t.Errorf("Terminal = false, want true (routed to %q)", tc.toStep)
			}
			if plan.TerminalStatus != tc.want {
				t.Errorf("TerminalStatus = %q, want %q", plan.TerminalStatus, tc.want)
			}
			if plan.Reason == "" {
				t.Errorf("Reason is empty, want a non-empty explanation")
			}
		})
	}
}

// TestPlanResumeDeliveryPendingRoutedToSuccess: a delivery_pending run whose
// derived active step is the reserved "success" terminal is settled (Terminal)
// but must NEVER be classified as succeeded. TerminalStatus stays
// delivery_pending so the resume path cannot CAS delivery_pending->succeeded
// and skip delivery; the plan points at the delivery step instead.
func TestPlanResumeDeliveryPendingRoutedToSuccess(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-delivery-pending-1"
	createRun(t, repo, runID)
	casRunStatus(t, repo, runID, RunStatusRunning)
	casRunStatus(t, repo, runID, RunStatusDeliveryPending)

	attempt := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
	completeAttempt(t, repo, runID, attempt, AttemptOutcome{
		Status:   AttemptStatusSucceeded,
		ToStepID: "success",
	})

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if plan.Run.Status != RunStatusDeliveryPending {
		t.Fatalf("Run.Status = %q, want %q", plan.Run.Status, RunStatusDeliveryPending)
	}
	if !plan.Terminal {
		t.Errorf("Terminal = false, want true (delivery_pending run is settled)")
	}
	if plan.TerminalStatus != RunStatusDeliveryPending {
		t.Errorf("TerminalStatus = %q, want %q (NOT %q)", plan.TerminalStatus, RunStatusDeliveryPending, RunStatusSucceeded)
	}
	if plan.TerminalStatus == RunStatusSucceeded {
		t.Errorf("TerminalStatus = %q, must never be %q for a delivery_pending run", plan.TerminalStatus, RunStatusSucceeded)
	}
	if !strings.Contains(plan.Reason, "delivery") {
		t.Errorf("Reason = %q, want a mention of delivery", plan.Reason)
	}
}

// TestPlanResumeMissingRun: PlanResume reports ErrNotFound for an absent run.
func TestPlanResumeMissingRun(t *testing.T) {
	repo := newTestRepo(t)
	_, err := PlanResume(context.Background(), repo, "wfr-does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("PlanResume err = %v, want ErrNotFound", err)
	}
}

// TestPlanResumeNextAttemptNoRespectsMax: the next attempt number is the max
// recorded attempt_no for the step plus one, even when earlier attempts are
// still in flight.
func TestPlanResumeNextAttemptNoRespectsMax(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-maxattempts-1"
	createRun(t, repo, runID)
	createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
	createAttempt(t, repo, runID, "plan", 2, "run-abc", "task-2")

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if plan.NextAttemptNo != 3 {
		t.Errorf("NextAttemptNo = %d, want 3", plan.NextAttemptNo)
	}
}

// TestPlanResumeRunActiveStepReflectsDerivedStep: the plan's Run.ActiveStepID
// is the derived active step — the transition target of the newest
// step-bearing event (a completion's to_step_id), not the initial step.
func TestPlanResumeRunActiveStepReflectsDerivedStep(t *testing.T) {
	repo := newTestRepo(t)
	runID := "wfr-derived-1"
	createRun(t, repo, runID)
	casRunStatus(t, repo, runID, RunStatusRunning)

	planAttempt := createAttempt(t, repo, runID, "plan", 1, "run-abc", "task-1")
	completeAttempt(t, repo, runID, planAttempt, AttemptOutcome{
		Status:   AttemptStatusSucceeded,
		ToStepID: "implement",
	})
	implAttempt := createAttempt(t, repo, runID, "implement", 1, "run-def", "task-2")
	completeAttempt(t, repo, runID, implAttempt, AttemptOutcome{
		Status:   AttemptStatusSucceeded,
		ToStepID: "review",
	})

	plan, err := PlanResume(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("PlanResume(%q): %v", runID, err)
	}
	if plan.Run.RunID != runID {
		t.Errorf("Run.RunID = %q, want %q", plan.Run.RunID, runID)
	}
	if plan.Run.Status != RunStatusRunning {
		t.Errorf("Run.Status = %q, want %q", plan.Run.Status, RunStatusRunning)
	}
	if plan.Run.ActiveStepID != "review" {
		t.Errorf("Run.ActiveStepID = %q, want %q (derived from completion to_step_id)", plan.Run.ActiveStepID, "review")
	}
	if plan.NextAttemptNo != 1 {
		t.Errorf("NextAttemptNo = %d, want 1 (fresh step %q)", plan.NextAttemptNo, "review")
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Errorf("AttemptsInFlight = %v, want empty", plan.AttemptsInFlight)
	}
	if plan.Terminal {
		t.Errorf("Terminal = true, want false")
	}
}
