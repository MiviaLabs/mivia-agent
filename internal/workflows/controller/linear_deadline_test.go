package controller

import (
	"context"
	"encoding/json"
	"errors"
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
