package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type faultWorkflowRepository struct {
	workflowledger.Repository
	getRunErr        error
	getRunFailAt     int
	getRunCalls      int
	getSnapshotErr   error
	compareErr       error
	listAttemptsErr  error
	createAttemptErr error
	getAttemptErr    error
	completeErr      error
}

type terminalAfterPendingRepository struct {
	workflowledger.Repository
	getRunCalls int
}

func (r *terminalAfterPendingRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.getRunCalls++
	run, err := r.Repository.GetRun(ctx, runID)
	if err == nil && r.getRunCalls == 1 {
		run.ActiveStepID = "success"
	}
	return run, err
}

func (r *faultWorkflowRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.getRunCalls++
	if r.getRunErr != nil && (r.getRunFailAt == 0 || r.getRunCalls == r.getRunFailAt) {
		return workflowledger.RunSnapshot{}, r.getRunErr
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *faultWorkflowRepository) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	if r.getSnapshotErr != nil {
		return nil, r.getSnapshotErr
	}
	return r.Repository.GetRunSnapshot(ctx, runID)
}

func (r *faultWorkflowRepository) CompareAndSetRunStatus(ctx context.Context, runID string, version uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if r.compareErr != nil {
		return r.compareErr
	}
	return r.Repository.CompareAndSetRunStatus(ctx, runID, version, status, finishedAt)
}

func (r *faultWorkflowRepository) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	if r.listAttemptsErr != nil {
		return nil, r.listAttemptsErr
	}
	return r.Repository.ListStepAttempts(ctx, runID)
}

func (r *faultWorkflowRepository) CreateStepAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error {
	if r.createAttemptErr != nil {
		return r.createAttemptErr
	}
	return r.Repository.CreateStepAttempt(ctx, attempt)
}

func (r *faultWorkflowRepository) GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error) {
	if r.getAttemptErr != nil {
		return workflowledger.StepAttempt{}, r.getAttemptErr
	}
	return r.Repository.GetStepAttempt(ctx, runID, attemptID)
}

func (r *faultWorkflowRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, version uint64, outcome workflowledger.AttemptOutcome) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, version, outcome)
}

func TestLinearControllerGeneratedIdentityAndExecutionSettings(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	timeoutSeconds := 4
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one", TimeoutSeconds: &timeoutSeconds}},
	}, map[string]any{"task": "build"}, "", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ctrl.RunID, "wfr-") {
		t.Fatalf("generated run ID = %q", ctrl.RunID)
	}
	if err := ctrl.SetForceResume(true); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("repeated Start: %v", err)
	}
	if err := ctrl.SetForceResume(false); err == nil {
		t.Fatal("force recovery changed after admission")
	}
	if err := ctrl.SetAdmission(Admission{}); err == nil {
		t.Fatal("admission changed after start")
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || !runner.calls[0].ForceResume || runner.calls[0].Timeout != 4*time.Second {
		t.Fatalf("step request = %+v", runner.calls)
	}
}

func TestLinearControllerReportsClockAndRunReadErrors(t *testing.T) {
	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	seed, err := NewLinearController(base, &linearRunner{}, wf, nil, nil, "wfr-read-errors", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.SetTimeSource(nil); err == nil {
		t.Fatal("nil clock was accepted")
	}
	if err := seed.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("run read failed")
	faults := &faultWorkflowRepository{Repository: base, getRunErr: sentinel}
	duplicate, err := NewLinearController(faults, &linearRunner{}, wf, nil, nil, seed.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Start(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("duplicate start error = %v", err)
	}

	runRead, err := NewLinearController(faults, &linearRunner{}, wf, nil, nil, "wfr-run-read-error", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runRead.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runRead.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("run read error = %v", err)
	}
}

func TestLinearControllerReportsDuplicateSnapshotReadError(t *testing.T) {
	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	seed, err := NewLinearController(base, &linearRunner{}, wf, nil, nil, "wfr-snapshot-read-error", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("snapshot read failed")
	faults := &faultWorkflowRepository{Repository: base, getSnapshotErr: sentinel}
	duplicate, err := NewLinearController(faults, &linearRunner{}, wf, nil, nil, seed.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Start(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("duplicate snapshot error = %v", err)
	}
}

func TestLinearControllerSettlesDeadlineAfterAdvanceReadFailure(t *testing.T) {
	base := workflowledger.NewMemoryRepository()
	faults := &faultWorkflowRepository{Repository: base, getRunErr: context.DeadlineExceeded, getRunFailAt: 4}
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(faults, runner, linearWorkflow(t), map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-advance-read-deadline", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
	stored, getErr := base.GetRun(context.Background(), ctrl.RunID)
	if getErr != nil || stored.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("stored run = %+v, error = %v", stored, getErr)
	}
}

func TestLinearControllerPersistsBindingFailure(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, nil, "wfr-missing-binding", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || run.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "missing input") {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v, error = %v", attempts, err)
	}
}

func TestLinearControllerPersistsBindingLimitAndRouteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*compiler.CompiledWorkflow)
		want   string
	}{
		{name: "binding limit", mutate: func(wf *compiler.CompiledWorkflow) { wf.Steps[0].Context[0].MaxBytes = 1 }, want: "exceeds 1 bytes"},
		{name: "missing route", mutate: func(wf *compiler.CompiledWorkflow) { wf.Transitions = wf.Transitions[1:] }, want: "no matching transition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := linearWorkflow(t)
			tc.mutate(wf)
			repo := workflowledger.NewMemoryRepository()
			runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
			ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
				"first": {Agent: agents.ResolvedAgent{Name: "one"}},
			}, map[string]any{"task": "build"}, "wfr-"+strings.ReplaceAll(tc.name, " ", "-"), []byte("snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			run, err := ctrl.Run(context.Background())
			if err == nil || run.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run = %+v, error = %v", run, err)
			}
		})
	}
}

type deadlineLinearRunner struct{}

func (deadlineLinearRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, context.DeadlineExceeded
}

func TestLinearControllerPersistsTimedOutAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, deadlineLinearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-step-timeout", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || run.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempts = %+v, error = %v", attempts, err)
	}
}

func TestStepPersistenceContextSurvivesCallerCancellation(t *testing.T) {
	caller, stopCaller := context.WithCancel(context.Background())
	stopCaller()
	persist, stopPersist := stepPersistenceContext(caller)
	defer stopPersist()
	if err := persist.Err(); err != nil {
		t.Fatalf("persistence context = %v", err)
	}
}

func TestLinearControllerRejectsInterruptedAttemptPastLimit(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Limits.MaxStepAttempts = 1
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-attempt-limit", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetRun(context.Background(), ctrl.RunID)
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err := repo.CompleteStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ctrl.Advance(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "exceeded max attempts") {
		t.Fatalf("run = %+v, error = %v", got, err)
	}
}

func TestLinearControllerTerminalReconciliationConflictAndTimeoutNoop(t *testing.T) {
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-terminal-helpers", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	conflict := workflowledger.RunSnapshot{RunID: ctrl.RunID, ActiveStepID: "success", Status: workflowledger.RunStatusFailed}
	if _, terminal, err := ctrl.reconcileTerminalRoute(context.Background(), conflict); !terminal || err == nil {
		t.Fatalf("terminal = %v, error = %v", terminal, err)
	}
	settled, err := ctrl.timeoutExpiredRun(context.Background(), workflowledger.RunSnapshot{RunID: ctrl.RunID, ActiveStepID: "first", Status: workflowledger.RunStatusSucceeded})
	if err != nil || settled.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("settled = %+v, error = %v", settled, err)
	}
}

func TestLinearDeadlineHelpersSurfaceRepositoryErrors(t *testing.T) {
	sentinel := errors.New("deadline repository failed")
	for _, tc := range []struct {
		name string
		run  workflowledger.RunSnapshot
		set  func(*faultWorkflowRepository)
		call func(*LinearController, workflowledger.RunSnapshot) error
	}{
		{
			name: "terminal status compare", run: workflowledger.RunSnapshot{RunID: "wfr-deadline-compare", ActiveStepID: "success", Status: workflowledger.RunStatusRunning, Version: 1},
			set: func(repo *faultWorkflowRepository) { repo.compareErr = sentinel },
			call: func(ctrl *LinearController, run workflowledger.RunSnapshot) error {
				_, _, err := ctrl.reconcileTerminalRoute(context.Background(), run)
				return err
			},
		},
		{
			name: "pending timeout compare", run: workflowledger.RunSnapshot{RunID: "wfr-timeout-compare", ActiveStepID: "first", Status: workflowledger.RunStatusPending, Version: 1},
			set: func(repo *faultWorkflowRepository) { repo.compareErr = sentinel },
			call: func(ctrl *LinearController, run workflowledger.RunSnapshot) error {
				_, err := ctrl.timeoutExpiredRun(context.Background(), run)
				return err
			},
		},
		{
			name: "pending timeout read", run: workflowledger.RunSnapshot{RunID: "wfr-timeout-read", ActiveStepID: "first", Status: workflowledger.RunStatusPending, Version: 1},
			set: func(repo *faultWorkflowRepository) { repo.getRunErr = sentinel },
			call: func(ctrl *LinearController, run workflowledger.RunSnapshot) error {
				_, err := ctrl.timeoutExpiredRun(context.Background(), run)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := workflowledger.NewMemoryRepository()
			seed := tc.run
			seed.Status = workflowledger.RunStatusPending
			if err := base.CreateRun(context.Background(), seed, []byte("snapshot")); err != nil {
				t.Fatal(err)
			}
			faults := &faultWorkflowRepository{Repository: base}
			tc.set(faults)
			ctrl, err := NewLinearController(faults, &linearRunner{}, linearWorkflow(t), nil, nil, tc.run.RunID, []byte("snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(ctrl, tc.run); !errors.Is(err, sentinel) {
				t.Fatalf("error = %v, want %v", err, sentinel)
			}
		})
	}
}

func TestLinearDeadlineHelpersAcceptMatchingTerminalState(t *testing.T) {
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-matching-terminal", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{RunID: ctrl.RunID, ActiveStepID: "success", Status: workflowledger.RunStatusSucceeded}
	settled, terminal, err := ctrl.reconcileTerminalRoute(context.Background(), run)
	if err != nil || !terminal || settled.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("settled = %+v, terminal = %v, error = %v", settled, terminal, err)
	}
}

func TestLinearTimeoutReconcilesRouteFoundAfterPendingTransition(t *testing.T) {
	base := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-timeout-late-route", ActiveStepID: "first", Status: workflowledger.RunStatusPending}
	if err := base.CreateRun(context.Background(), run, []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	repo := &terminalAfterPendingRepository{Repository: base}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, run.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := base.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := ctrl.timeoutExpiredRun(context.Background(), stored)
	if err != nil || settled.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("settled = %+v, error = %v", settled, err)
	}
}

func TestLinearExecutionSurfacesAttemptRepositoryErrors(t *testing.T) {
	sentinel := errors.New("attempt repository failed")
	for _, tc := range []struct {
		name string
		set  func(*faultWorkflowRepository)
	}{
		{name: "list", set: func(repo *faultWorkflowRepository) { repo.listAttemptsErr = sentinel }},
		{name: "create", set: func(repo *faultWorkflowRepository) { repo.createAttemptErr = sentinel }},
		{name: "read", set: func(repo *faultWorkflowRepository) { repo.getAttemptErr = sentinel }},
		{name: "complete", set: func(repo *faultWorkflowRepository) { repo.completeErr = sentinel }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := workflowledger.NewMemoryRepository()
			faults := &faultWorkflowRepository{Repository: base}
			tc.set(faults)
			runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
			ctrl, err := NewLinearController(faults, runner, linearWorkflow(t), map[string]StepRuntime{
				"first": {Agent: agents.ResolvedAgent{Name: "one"}},
			}, map[string]any{"task": "build"}, "wfr-attempt-error-"+tc.name, []byte("snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, _, err = ctrl.Advance(context.Background())
			if !errors.Is(err, sentinel) && !strings.Contains(err.Error(), sentinel.Error()) {
				t.Fatalf("error = %v, want %v", err, sentinel)
			}
		})
	}
}

func TestLinearExecutionSurfacesInterruptedRetryRepositoryErrors(t *testing.T) {
	sentinel := errors.New("retry repository failed")
	for _, tc := range []struct {
		name string
		set  func(*faultWorkflowRepository)
	}{
		{name: "create", set: func(repo *faultWorkflowRepository) { repo.createAttemptErr = sentinel }},
		{name: "read", set: func(repo *faultWorkflowRepository) { repo.getAttemptErr = sentinel }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := workflowledger.NewMemoryRepository()
			faults := &faultWorkflowRepository{Repository: base}
			ctrl, err := NewLinearController(faults, &linearRunner{}, linearWorkflow(t), map[string]StepRuntime{
				"first": {Agent: agents.ResolvedAgent{Name: "one"}},
			}, map[string]any{"task": "build"}, "wfr-retry-error-"+tc.name, []byte("snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			run, _ := base.GetRun(context.Background(), ctrl.RunID)
			if err := base.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
				t.Fatal(err)
			}
			attempt := workflowledger.StepAttempt{AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
			if err := base.CreateStepAttempt(context.Background(), attempt); err != nil {
				t.Fatal(err)
			}
			stored, _ := base.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
			if err := base.CompleteStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusInterrupted}); err != nil {
				t.Fatal(err)
			}
			tc.set(faults)
			_, _, err = ctrl.Advance(context.Background())
			if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
				t.Fatalf("error = %v, want %v", err, sentinel)
			}
		})
	}
}

func TestLinearExecutionSurfacesFinalStatusCompareError(t *testing.T) {
	base := workflowledger.NewMemoryRepository()
	faults := &faultWorkflowRepository{Repository: base}
	runner := &linearRunner{outputs: map[string]json.RawMessage{
		"first":  json.RawMessage(`{"ok":true}`),
		"second": json.RawMessage(`{"done":true}`),
	}}
	ctrl, err := NewLinearController(faults, runner, linearWorkflow(t), map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-final-status-error", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := base.GetRun(context.Background(), ctrl.RunID)
	if err := base.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if _, done, err := ctrl.Advance(context.Background()); err != nil || done {
		t.Fatalf("first advance = done %v, error %v", done, err)
	}
	sentinel := errors.New("final status failed")
	faults.compareErr = sentinel
	_, _, err = ctrl.Advance(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
}
