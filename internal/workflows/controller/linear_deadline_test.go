package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func deadlineWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf, err := definition.Compile(&definition.WorkflowFile{
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

// TestDeadlineExpiryClosesHumanGateAttemptWithErrorRef verifies that a human
// gate attempt left running at deadline expiry completes as timed_out with a
// persisted deadline cause and a step_completed progress event.
func TestDeadlineExpiryClosesHumanGateAttemptWithErrorRef(t *testing.T) {
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, humanOnlyWorkflow(t), nil, map[string]any{"task": "x"}, "wfr-deadline-human", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err=%v, want waiting_approval", got, err)
	}
	ctrl.now = func() time.Time { return start.Add(2 * time.Hour) }
	settled, err := ctrl.Run(context.Background())
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expired run: %v", err)
	}
	if settled.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out", settled.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, ok := latestAttempt(attempts, "approve_me")
	if !ok {
		t.Fatal("missing human gate attempt")
	}
	if attempt.Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("human attempt status = %q, want timed_out", attempt.Status)
	}
	if attempt.ErrorRef == "" {
		t.Fatal("human attempt ErrorRef is empty, want deadline detail")
	}
	raw, err := repo.LoadContent(context.Background(), attempt.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "deadline exceeded") {
		t.Fatalf("error ref content = %q, want text containing 'deadline exceeded'", raw)
	}
	var completed bool
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted && e.StepID == "approve_me" && e.Detail == string(workflowledger.AttemptStatusTimedOut) {
			completed = true
		}
	}
	if !completed {
		t.Fatal("no step_completed progress event for the timed out human gate")
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

// blockingContextVerifier blocks until the caller context is done, then
// returns a host-class failure wrapping the context error. It simulates a
// sandboxed verifier that hits the run deadline.
type blockingContextVerifier struct {
	started chan struct{}
}

func (v *blockingContextVerifier) Name() string { return "blocking-context" }

func (v *blockingContextVerifier) Verify(ctx context.Context, _ definition.Request) (definition.Result, error) {
	close(v.started)
	<-ctx.Done()
	return definition.Result{
		Status: "failed",
		Checks: []definition.Check{{Name: "sandbox", Status: "failed", Class: "host", Detail: "host verifier setup failed"}},
	}, fmt.Errorf("host verifier setup failed: %w", ctx.Err())
}

// TestEvidenceGateVerifierHostFailureFromContextErrorTimesOut verifies that a
// verifier host failure caused by the caller context expiring is settled as a
// run timeout, not as a fabricated host failure.
func TestEvidenceGateVerifierHostFailureFromContextErrorTimesOut(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-timeout", InitialStep: "verify",
		Inputs:      map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits:      definition.Limits{MaxStepAttempts: 4},
		Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "blocking-context", OnFailure: "failure"}},
		Transitions: []definition.Transition{{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	v := &blockingContextVerifier{started: make(chan struct{})}
	if err := cat.Register(v); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence-timeout", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-v.started
		cancel()
	}()
	got, done, err := ctrl.Advance(ctx)
	if !done {
		t.Fatalf("advance done=%v, want true", done)
	}
	if got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out", got.Status)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context error", err)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok {
		t.Fatal("missing verify attempt")
	}
	if verify.ErrorRef != "" {
		raw, loadErr := repo.LoadContent(context.Background(), verify.ErrorRef)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if strings.Contains(string(raw), "host failure") {
			t.Fatalf("error ref contains host failure: %s", raw)
		}
	}
	// The admitted verify attempt must be settled, never left Running on a
	// terminal run: the deadline/cancel branch completes it as timed_out
	// before the run reaches its terminal state.
	if verify.Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("verify attempt status = %q, want %q (settled, not left Running)", verify.Status, workflowledger.AttemptStatusTimedOut)
	}
}

// ctxAwareRepo wraps a repository and fails GetLoopCounters when the caller
// context is already canceled. It proves that evidence-gate routing uses the
// detached persistence context, not the caller context.
type ctxAwareRepo struct {
	workflowledger.Repository
}

func (r *ctxAwareRepo) GetLoopCounters(ctx context.Context, runID string) ([]workflowledger.LoopCounter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Repository.GetLoopCounters(ctx, runID)
}

// TestEvidenceGateLoopTransitionUsesDetachedContext verifies that an
// evidence-gate loop transition at the run deadline still routes using the
// detached persistence context instead of the canceled caller context.
func TestEvidenceGateLoopTransitionUsesDetachedContext(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-loop-context", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-passes", OnFailure: "failure"},
			{ID: "repair", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "repair", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}, Loop: "fix", MaxIterations: 4},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "approved"}}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-passes",
		result: definition.Result{Status: "passed", Checks: []definition.Check{{Name: "test", Status: "passed"}}},
	}); err != nil {
		t.Fatal(err)
	}
	repo := &ctxAwareRepo{Repository: workflowledger.NewMemoryRepository()}
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-loop-context", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, done, err := ctrl.Advance(ctx)
	if err != nil {
		t.Fatalf("advance err=%v", err)
	}
	if done {
		t.Fatalf("advance done=%v, want false", done)
	}
	if got.Status != workflowledger.RunStatusRunning {
		t.Fatalf("status = %q, want running", got.Status)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok {
		t.Fatal("missing verify attempt")
	}
	if verify.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("verify status = %q, want succeeded", verify.Status)
	}
	if verify.ToStepID != "repair" {
		t.Fatalf("verify route = %q, want repair", verify.ToStepID)
	}
}
