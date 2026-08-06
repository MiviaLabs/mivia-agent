package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func deadlineWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf, err := compiler.Compile(&definition.WorkflowFile{
		Version: 1, Name: "deadline", InitialStep: "work",
		Steps:       []definition.Step{{ID: "work", Kind: "agent", Agent: "worker"}},
		Transitions: []definition.Transition{{From: "work", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}},
		Limits:      definition.Limits{MaxDurationSeconds: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func newDeadlineController(t *testing.T, repo workflowledger.Repository, runner AgentStepRunner, runID string, now func() time.Time) *LinearController {
	t.Helper()
	ctrl, err := NewLinearController(repo, runner, deadlineWorkflow(t), map[string]StepRuntime{
		"work": {Agent: agents.ResolvedAgent{Name: "worker"}},
	}, nil, runID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetTimeSource(now); err != nil {
		t.Fatal(err)
	}
	return ctrl
}

func persistDeadlineRoute(t *testing.T, repo workflowledger.Repository, runID, route string) {
	t.Helper()
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-work-1", RunID: runID, StepID: "work", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(context.Background(), runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(context.Background(), runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusSucceeded, ToStepID: route}); err != nil {
		t.Fatal(err)
	}
}

func TestRunExpiredDeadlinePreservesDurableTerminalRoute(t *testing.T) {
	for _, tc := range []struct {
		route string
		want  workflowledger.RunStatus
	}{{route: "success", want: workflowledger.RunStatusSucceeded}, {route: "failure", want: workflowledger.RunStatusFailed}} {
		t.Run(tc.route, func(t *testing.T) {
			start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
			repo := workflowledger.NewMemoryRepository()
			runner := &linearRunner{}
			ctrl := newDeadlineController(t, repo, runner, "wfr-expired-route-"+tc.route, func() time.Time { return start })
			if err := ctrl.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			persistDeadlineRoute(t, repo, ctrl.RunID, tc.route)
			ctrl.now = func() time.Time { return start.Add(time.Hour) }
			got, err := ctrl.Run(context.Background())
			if err != nil || got.Status != tc.want {
				t.Fatalf("run = %+v, err=%v, want status %q", got, err, tc.want)
			}
			if len(runner.calls) != 0 {
				t.Fatal("terminal route dispatched work")
			}
		})
	}
}

func TestAdvanceExpiredDeadlineDoesNotDispatch(t *testing.T) {
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{}
	ctrl := newDeadlineController(t, repo, runner, "wfr-advance-expired", func() time.Time { return start })
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctrl.now = func() time.Time { return start.Add(time.Hour) }
	got, done, err := ctrl.Advance(context.Background())
	if !done || !errors.Is(err, context.DeadlineExceeded) || got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("advance = %+v, done=%v, err=%v", got, done, err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("expired advance dispatched work")
	}
}

type deadlineGetRunRepository struct {
	workflowledger.Repository
	mu       sync.Mutex
	getCalls int
	failAt   int
}

func (r *deadlineGetRunRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.mu.Lock()
	r.getCalls++
	fail := r.getCalls == r.failAt
	r.mu.Unlock()
	if fail {
		return workflowledger.RunSnapshot{}, context.DeadlineExceeded
	}
	return r.Repository.GetRun(ctx, runID)
}

func TestRunDeadlineErrorPreservesRoutePersistedByAttempt(t *testing.T) {
	base := workflowledger.NewMemoryRepository()
	repo := &deadlineGetRunRepository{Repository: base, failAt: 4}
	runner := &linearRunner{outputs: map[string]json.RawMessage{"work": json.RawMessage(`{"ok":true}`)}}
	ctrl := newDeadlineController(t, repo, runner, "wfr-route-before-deadline-error", time.Now)
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err=%v", got, err)
	}
}

// vanishingRunRepo wraps a repository to simulate GetRun returning a
// zero-value RunSnapshot without an error on a specific call, modelling a
// future repository implementation that may not return ErrNotFound.
type vanishingRunRepo struct {
	workflowledger.Repository
	mu         sync.Mutex
	getRunCall int
	vanishAt   int // GetRun call number that returns a zero-value snapshot
	runID      string
}

func (r *vanishingRunRepo) GetRun(ctx context.Context, id string) (workflowledger.RunSnapshot, error) {
	r.mu.Lock()
	r.getRunCall++
	vanish := id == r.runID && r.getRunCall == r.vanishAt
	r.mu.Unlock()
	if vanish {
		return workflowledger.RunSnapshot{}, nil
	}
	return r.Repository.GetRun(ctx, id)
}

// TestReconcileTerminalRouteVanishingRun verifies that reconcileTerminalRoute
// guards against a repository that returns a zero-value RunSnapshot (with no
// error) after the waiting_approval → running CAS, producing a clear "not found"
// error instead of proceeding with a zero version.
func TestReconcileTerminalRouteVanishingRun(t *testing.T) {
	ctx := context.Background()
	base := workflowledger.NewMemoryRepository()
	wf := humanDeliveryWorkflow(t, false)
	runID := "wfr-vanishing"

	// Start the run and advance it to waiting_approval.
	ctrl, err := NewLinearController(base, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q, want waiting_approval", got.Status)
	}

	// Simulate a partial crash recovery: the approval was processed (the
	// attempt routes to "success", moving ActiveStepID to the terminal), but
	// the final status transition never completed, leaving the run in
	// waiting_approval at the "success" terminal step.
	attempts, err := base.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	approvalAttempt, ok := latestAttempt(attempts, "approve_me")
	if !ok {
		t.Fatal("missing approval attempt")
	}
	output := map[string]any{"decision": "approved"}
	raw, _ := json.Marshal(output)
	route := RouteDecision{ToStepID: "success", MatchDigest: "d", DecisionJSON: raw}
	if err := CompleteExistingStepResult(ctx, base, approvalAttempt, AgentStepResult{Output: raw, ValidatedOutput: output}, workflowledger.AttemptStatusSucceeded, route); err != nil {
		t.Fatal(err)
	}

	// After completing the attempt, the derived ActiveStepID should be "success".
	run, err := base.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ActiveStepID != "success" {
		t.Fatalf("active step = %q, want success", run.ActiveStepID)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q, want waiting_approval", run.Status)
	}

	// Wrap the repo so that the GetRun call inside reconcileTerminalRoute's
	// waiting_approval block returns a zero-value snapshot without an error.
	repo := &vanishingRunRepo{Repository: base, vanishAt: 1, runID: runID}

	ctrl2, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}

	// Call reconcileTerminalRoute with the run in waiting_approval at "success".
	_, terminal, err := ctrl2.reconcileTerminalRoute(ctx, run)
	if err == nil {
		t.Fatal("expected an error for vanishing run, got nil")
	}
	if !terminal {
		t.Fatal("expected terminal=true")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want error containing 'not found'", err)
	}
}
