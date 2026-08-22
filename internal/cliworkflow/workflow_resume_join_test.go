package cliworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowResumeJoinRunner is a controller runner for resume tests: it can
// JOIN a previously dispatched child by its recorded identity (StepRunJoiner)
// and records any fresh dispatch.
type workflowResumeJoinRunner struct {
	mu       sync.Mutex
	joined   []controller.AgentStepRequest
	dispatch []controller.AgentStepRequest
	children map[string]controller.AgentStepResult // by TaskID
}

type workflowResumeRaceClaimRepository struct {
	workflowledger.Repository
	contender string
}

func (r *workflowResumeRaceClaimRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, _ string, _ time.Duration) error {
	if err := r.Repository.ClaimRun(ctx, runID, r.contender); err != nil {
		return err
	}
	return workflowledger.ErrClaimNotHeld
}

func (r *workflowResumeJoinRunner) JoinStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.joined = append(r.joined, req)
	result, ok := r.children[req.TaskID]
	if !ok {
		return controller.AgentStepResult{}, false, nil
	}
	// Mirror the production runner: a child that did not complete carries an
	// error alongside its terminal status.
	if result.Status != "" && result.Status != "completed" {
		return result, true, fmt.Errorf("child %s reported %s", req.TaskID, result.Status)
	}
	return result, true, nil
}

func (r *workflowResumeJoinRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatch = append(r.dispatch, req)
	return controller.AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// TestExecuteWorkflowResumeJoinsInFlightAttempt: a forced resume of a run with
// an in-flight attempt whose recorded coordinator child already COMPLETED must
// JOIN the child — the attempt settles with the child's outcome and route and
// the step is never re-executed under a new coordinator identity. This is the
// resume boundary's consumption of PlanResume.AttemptsInFlight.
func TestExecuteWorkflowResumeJoinsInFlightAttempt(t *testing.T) {
	root, repo, run, runner, snapshot, ctrl := newJoinResumeFixture(t)
	_ = snapshot
	_ = ctrl
	ctx := context.Background()
	configPath := filepath.Join(root, "config.toml")
	var stdout bytes.Buffer
	if err := ExecuteWorkflowResume(run.RunID, root, configPath, true, false, false, false, &stdout, io.Discard); err != nil {
		t.Fatalf("ExecuteWorkflowResume(force) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("resume output = %q, want status=succeeded", stdout.String())
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.joined) != 1 || runner.joined[0].CoordinatorRunID != "coord-rec" || runner.joined[0].TaskID != "task-rec" {
		t.Fatalf("joined requests = %+v, want the recorded child identity", runner.joined)
	}
	for _, req := range runner.dispatch {
		if req.StepID == "one" {
			t.Fatalf("resume re-executed step one with a fresh child: %+v", req)
		}
	}
	attempts, err := repo.ListStepAttempts(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (joined one + fresh two)", len(attempts))
	}
	one := attempts[0]
	if one.AttemptID != "wfa-one-1" || one.Status != workflowledger.AttemptStatusSucceeded || one.ToStepID != "two" {
		t.Fatalf("joined attempt = %+v, want succeeded routed to two", one)
	}
	if one.CoordinatorRunID != "coord-rec" || one.TaskID != "task-rec" {
		t.Fatalf("joined attempt identity changed: %+v", one)
	}
	if attempts[1].StepID != "two" || attempts[1].AttemptNo != 1 {
		t.Fatalf("fresh attempt = %+v, want step two No 1", attempts[1])
	}
}

// TestPrepareWorkflowResumeExecutionDoesNotClearConcurrentClaim proves that a
// normal resume never removes a claim that appears after the lease check.
func TestPrepareWorkflowResumeExecutionDoesNotClearConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	base := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = base.Close() })
	runID := "wfr-resume-race"
	if err := base.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	repo := &workflowResumeRaceClaimRepository{Repository: base, contender: "other-executor"}
	built := WorkflowControllerBuild{Controller: &controller.LinearController{Holder: "resuming-executor"}}
	err := prepareWorkflowResumeExecution(ctx, built, repo, runID, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("prepareWorkflowResumeExecution() error = %v, want active claim refusal", err)
	}
	if err := base.ClaimRun(ctx, runID, "resuming-executor"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("resuming executor claim = %v, want held by concurrent executor", err)
	}
}

// newJoinResumeRun scaffolds the shared resume-join fixture: the run (with an
// optional run deadline), the seeded in-flight attempt, the controller over the
// given runner, and the resume boundary seams. The runner is injected so join
// tests can substitute a join that never settles (workflowResumeHangingJoinRunner).
func newJoinResumeRun(t *testing.T, runner controller.AgentStepRunner, deadline *time.Time) (root string, repo workflowledger.Repository, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot, ctrl *controller.LinearController) {
	t.Helper()
	root, _ = newForcedResumeFixture(t)
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot = newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run = workflowledger.RunSnapshot{
		RunID: "wfr-join-cli", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
		DeadlineAt: deadline,
	}
	ctx := context.Background()
	repo = workflowledger.NewMemoryRepository()
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// Seed a RUNNING attempt whose coordinator child already ran (the join is
	// expected to settle it from the child's recorded outcome).
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: run.RunID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-rec", TaskID: "task-rec",
	}); err != nil {
		t.Fatal(err)
	}
	steps := map[string]controller.StepRuntime{
		"one": {Agent: agents.ResolvedAgent{Name: "one"}, ProviderName: "openrouter", Model: "test/model"},
		"two": {Agent: agents.ResolvedAgent{Name: "two"}, ProviderName: "openrouter", Model: "test/model"},
	}
	ctrl, err = controller.NewLinearController(repo, runner, compiled, steps, map[string]any{"task": "test"}, run.RunID, rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	originalOpen := WorkflowResumeOpenStore
	originalBuild := workflowResumeBuild
	t.Cleanup(func() {
		WorkflowResumeOpenStore = originalOpen
		workflowResumeBuild = originalBuild
	})
	WorkflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *definition.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, []byte, *workflowledger.RunSnapshot, map[string]bool, *skills.Registry) (WorkflowControllerBuild, error) {
		return WorkflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(snapshot.Inputs)},
		}, nil
	}
	return root, repo, run, snapshot, ctrl
}

// newJoinResumeFixture builds the run, in-flight attempt, join-capable runner
// and controller for the resume-join scenario, wiring the resume boundary seams.
func newJoinResumeFixture(t *testing.T) (root string, repo workflowledger.Repository, run workflowledger.RunSnapshot, runner *workflowResumeJoinRunner, snapshot workflowledger.Snapshot, ctrl *controller.LinearController) {
	t.Helper()
	runner = &workflowResumeJoinRunner{children: map[string]controller.AgentStepResult{
		"task-rec": {
			CoordinatorRunID: "coord-rec", TaskID: "task-rec",
			Output: json.RawMessage(`{"ok":true}`), EvidenceJSON: []byte(`[]`), Status: "completed",
		},
	}}
	root, repo, run, snapshot, ctrl = newJoinResumeRun(t, runner, nil)
	return root, repo, run, runner, snapshot, ctrl
}

// TestExecuteWorkflowResumeRefusesNilControllerWithInFlightAttempts: the
// resume boundary refuses to proceed when in-flight attempts exist but the
// controller build produced no controller to join them.
func TestExecuteWorkflowResumeRefusesNilControllerWithInFlightAttempts(t *testing.T) {
	root, _ := newForcedResumeFixture(t)
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-join-nil", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: run.RunID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-rec", TaskID: "task-rec",
	}); err != nil {
		t.Fatal(err)
	}
	originalOpen := WorkflowResumeOpenStore
	originalBuild := workflowResumeBuild
	originalAdmission := WorkflowResumeSetAdmission
	originalForce := WorkflowResumeSetForce
	t.Cleanup(func() {
		WorkflowResumeOpenStore = originalOpen
		workflowResumeBuild = originalBuild
		WorkflowResumeSetAdmission = originalAdmission
		WorkflowResumeSetForce = originalForce
	})
	WorkflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *definition.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, []byte, *workflowledger.RunSnapshot, map[string]bool, *skills.Registry) (WorkflowControllerBuild, error) {
		return WorkflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, nil
	}
	WorkflowResumeSetAdmission = func(WorkflowControllerBuild) error { return nil }
	WorkflowResumeSetForce = func(WorkflowControllerBuild) error { return nil }
	err = ExecuteWorkflowResume(run.RunID, root, filepath.Join(root, "config.toml"), true, false, false, false, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot join") {
		t.Fatalf("ExecuteWorkflowResume() error = %v, want nil-controller join refusal", err)
	}
}

// workflowResumeHangingJoinRunner is a controller runner whose JoinStep never
// settles on its own: it blocks until the caller's context expires, mirroring
// a production coordinator child whose run never reaches a terminal outcome.
// RunStep completes instantly so a run that escapes the join still progresses.
type workflowResumeHangingJoinRunner struct {
	mu     sync.Mutex
	joined []controller.AgentStepRequest
}

func (r *workflowResumeHangingJoinRunner) JoinStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, bool, error) {
	r.mu.Lock()
	r.joined = append(r.joined, req)
	r.mu.Unlock()
	<-ctx.Done()
	return controller.AgentStepResult{}, false, ctx.Err()
}

func (r *workflowResumeHangingJoinRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	return controller.AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// resumeJoinMustReturn runs ExecuteWorkflowResume and fails the test if it does
// not return within the anti-hang window: the whole point of the join bound is
// that a child that never settles cannot park resume forever.
func resumeJoinMustReturn(t *testing.T, runID, root string, stdout io.Writer) error {
	t.Helper()
	type resumeResult struct {
		err error
	}
	done := make(chan resumeResult, 1)
	go func() {
		err := ExecuteWorkflowResume(runID, root, filepath.Join(root, "config.toml"), true, false, false, false, stdout, io.Discard)
		done <- resumeResult{err: err}
	}()
	select {
	case res := <-done:
		return res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("ExecuteWorkflowResume did not return within 5s: the in-flight join hung without a bound")
		return nil
	}
}

// TestExecuteWorkflowResumeJoinBoundedByRunDeadline: a forced resume whose
// in-flight join never settles (the recorded child's coordinator run never
// reaches a terminal outcome) must not park the resume command past the run's
// own deadline: the join runs under a context bounded by time.Until(deadline)
// and, on expiry, resume returns a clear error with the attempt still in-flight
// for the controller's normal reconciliation.
func TestExecuteWorkflowResumeJoinBoundedByRunDeadline(t *testing.T) {
	runner := &workflowResumeHangingJoinRunner{}
	deadline := time.Now().Add(150 * time.Millisecond)
	root, repo, run, _, ctrl := newJoinResumeRun(t, runner, &deadline)
	_ = ctrl
	start := time.Now()
	err := resumeJoinMustReturn(t, run.RunID, root, io.Discard)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("ExecuteWorkflowResume took %v to return; want within the run deadline bound", elapsed)
	}
	if err == nil {
		t.Fatalf("ExecuteWorkflowResume() error = nil, want the join-bound expiry error")
	}
	if !strings.Contains(err.Error(), "join in-flight attempt") || !strings.Contains(err.Error(), "wfa-one-1") {
		t.Fatalf("ExecuteWorkflowResume() error = %v, want a clear in-flight join error", err)
	}
	runner.mu.Lock()
	if len(runner.joined) != 1 || runner.joined[0].TaskID != "task-rec" {
		t.Fatalf("joined requests = %+v, want the recorded child identity", runner.joined)
	}
	runner.mu.Unlock()
	// The expired join leaves the run reconcilable: the attempt is still
	// in-flight for the controller's normal reconciliation (Advance interrupts
	// and re-dispatches it under the run claim), never settled or corrupted.
	attempts, err := repo.ListStepAttempts(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].AttemptID != "wfa-one-1" || attempts[0].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("attempts = %+v, want wfa-one-1 left in-flight/running", attempts)
	}
}

// workflowResumeSlowJoinRunner settles genuinely, but only after a delay -
// long enough to prove resume waited for the real outcome instead of cutting
// the join off at a short fixed bound.
type workflowResumeSlowJoinRunner struct {
	mu     sync.Mutex
	joined []controller.AgentStepRequest
	delay  time.Duration
	result controller.AgentStepResult
}

func (r *workflowResumeSlowJoinRunner) JoinStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, bool, error) {
	r.mu.Lock()
	r.joined = append(r.joined, req)
	r.mu.Unlock()
	select {
	case <-time.After(r.delay):
		return r.result, true, nil
	case <-ctx.Done():
		return controller.AgentStepResult{}, false, ctx.Err()
	}
}

func (r *workflowResumeSlowJoinRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	return controller.AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// TestExecuteWorkflowResumeNoDeadlineJoinAwaitsGenuineCompletion is the
// regression for wfr-HHR2BWDUAK2PETK7: a run with no configured deadline
// (max_duration_seconds=0, unlimited) must NOT have its in-flight join cut
// short by an artificial fixed bound of resume's own invention. Before the
// fix, workflowResumeJoinCtx substituted a fixed 10-minute
// workflowResumeJoinBound for "no deadline," and that bound flowed all the
// way into the live coordinator join: it fired at a fixed wall-clock mark
// even while the child was still actively heartbeating, canceled the
// still-progressing child, and let the canceled outcome settle as a genuine
// terminal result - the run was marked permanently timed_out on work that
// was never stalled. This pins that a slow-but-genuinely-completing join is
// awaited to its real outcome, not abandoned early.
func TestExecuteWorkflowResumeNoDeadlineJoinAwaitsGenuineCompletion(t *testing.T) {
	delay := 300 * time.Millisecond
	runner := &workflowResumeSlowJoinRunner{
		delay: delay,
		result: controller.AgentStepResult{
			CoordinatorRunID: "coord-rec", TaskID: "task-rec",
			Output: json.RawMessage(`{"ok":true}`), EvidenceJSON: []byte(`[]`), Status: "completed",
		},
	}
	root, repo, run, _, _ := newJoinResumeRun(t, runner, nil)
	start := time.Now()
	var stdout bytes.Buffer
	err := ExecuteWorkflowResume(run.RunID, root, filepath.Join(root, "config.toml"), true, false, false, false, &stdout, io.Discard)
	elapsed := time.Since(start)
	if elapsed < delay {
		t.Fatalf("ExecuteWorkflowResume returned after %v, before the child's own %v settle delay - the join was cut short by something other than the child's real completion", elapsed, delay)
	}
	if err != nil {
		t.Fatalf("ExecuteWorkflowResume() error = %v, want the slow-but-genuine join to succeed", err)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("resume output = %q, want status=succeeded", stdout.String())
	}
	runner.mu.Lock()
	if len(runner.joined) != 1 || runner.joined[0].TaskID != "task-rec" {
		t.Fatalf("joined requests = %+v, want the recorded child identity", runner.joined)
	}
	runner.mu.Unlock()
	// The joined attempt (step "one") settles succeeded and the run continues
	// to dispatch step "two" fresh, exactly like a normal join - matching
	// TestExecuteWorkflowResumeJoinsInFlightAttempt's fixture behavior.
	attempts, err := repo.ListStepAttempts(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (joined one + fresh two)", len(attempts))
	}
	one := attempts[0]
	if one.AttemptID != "wfa-one-1" || one.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("joined attempt = %+v, want succeeded from the slow-but-genuine join", one)
	}
}

// workflowResumeBlockingJoinRunner is a controller runner whose JoinStep blocks
// until the caller unblocks it. It mirrors a coordinator child whose run takes
// longer than the claim lease to settle.
type workflowResumeBlockingJoinRunner struct {
	mu     sync.Mutex
	joined []controller.AgentStepRequest
	block  chan struct{}
}

func (r *workflowResumeBlockingJoinRunner) JoinStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, bool, error) {
	r.mu.Lock()
	r.joined = append(r.joined, req)
	r.mu.Unlock()
	select {
	case <-r.block:
		return controller.AgentStepResult{}, false, nil
	case <-ctx.Done():
		return controller.AgentStepResult{}, false, ctx.Err()
	}
}

func (r *workflowResumeBlockingJoinRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	return controller.AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// newHandoffHeartbeatFixture creates a running workflow with one in-flight
// attempt and a controller over the given runner, for direct tests of
// prepareWorkflowResumeExecution's claim handoff heartbeat.
func newHandoffHeartbeatFixture(t *testing.T, runner controller.AgentStepRunner) (repo *workflowledger.StorageRepository, runID string, ctrl *controller.LinearController) {
	t.Helper()
	ctx := context.Background()
	repo = workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID = "wfr-handoff-heartbeat"

	root, _ := newForcedResumeFixture(t)
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	run := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-rec", TaskID: "task-rec",
	}); err != nil {
		t.Fatal(err)
	}
	steps := map[string]controller.StepRuntime{
		"one": {Agent: agents.ResolvedAgent{Name: "one"}, ProviderName: "openrouter", Model: "test/model"},
		"two": {Agent: agents.ResolvedAgent{Name: "two"}, ProviderName: "openrouter", Model: "test/model"},
	}
	ctrl, err = controller.NewLinearController(repo, runner, compiled, steps, map[string]any{"task": "test"}, runID, rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	return repo, runID, ctrl
}

// TestPrepareWorkflowResumeExecutionHeartbeatsHandoffClaim proves the pre-Run
// handoff claim is refreshed during a long in-flight join. Without the
// heartbeat, a third party could take over the "expired" claim while the join
// is still settling attempts.
func TestPrepareWorkflowResumeExecutionHeartbeatsHandoffClaim(t *testing.T) {
	repo, runID, ctrl := newHandoffHeartbeatFixture(t, &workflowResumeBlockingJoinRunner{block: make(chan struct{})})
	ctx := context.Background()

	originalInterval := workflowResumeClaimHeartbeatInterval
	t.Cleanup(func() { workflowResumeClaimHeartbeatInterval = originalInterval })
	workflowResumeClaimHeartbeatInterval = time.Millisecond

	refreshed := make(chan struct{}, 8)
	originalHook := workflowResumeClaimHeartbeatHook
	t.Cleanup(func() { workflowResumeClaimHeartbeatHook = originalHook })
	workflowResumeClaimHeartbeatHook = func() { refreshed <- struct{}{} }

	runner := ctrl.Runner.(*workflowResumeBlockingJoinRunner)

	errCh := make(chan error, 1)
	go func() {
		errCh <- prepareWorkflowResumeExecution(ctx, WorkflowControllerBuild{Controller: ctrl}, repo, runID, true, io.Discard)
	}()

	// Wait for several successful heartbeats so the claim has been refreshed.
	for range 3 {
		select {
		case <-refreshed:
		case <-time.After(5 * time.Second):
			t.Fatal("heartbeat did not refresh within the test timeout")
		}
	}

	// The heartbeat keeps the claim fresh: a third-party takeover using a short
	// expiry window must be refused.
	if err := repo.TakeoverExpiredRunClaim(ctx, runID, "third-party", 50*time.Millisecond); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("third-party expired takeover = %v, want ErrClaimHeld (heartbeat kept resumer alive)", err)
	}

	runner.mu.Lock()
	if len(runner.joined) != 1 || runner.joined[0].TaskID != "task-rec" {
		t.Fatalf("joined requests = %+v, want the recorded child identity", runner.joined)
	}
	runner.mu.Unlock()

	close(runner.block)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("prepareWorkflowResumeExecution() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prepareWorkflowResumeExecution did not return after the join was unblocked")
	}
}

// TestPrepareWorkflowResumeExecutionCancelsJoinWhenClaimLost proves the
// heartbeat fails fast when another holder takes the claim: it cancels the join
// context so the resume does not wait up to the full join bound only to fail a
// fenced settle write.
func TestPrepareWorkflowResumeExecutionCancelsJoinWhenClaimLost(t *testing.T) {
	repo, runID, ctrl := newHandoffHeartbeatFixture(t, &workflowResumeBlockingJoinRunner{block: make(chan struct{})})
	ctx := context.Background()

	originalInterval := workflowResumeClaimHeartbeatInterval
	t.Cleanup(func() { workflowResumeClaimHeartbeatInterval = originalInterval })
	workflowResumeClaimHeartbeatInterval = time.Millisecond

	refreshed := make(chan struct{}, 8)
	originalHook := workflowResumeClaimHeartbeatHook
	t.Cleanup(func() { workflowResumeClaimHeartbeatHook = originalHook })
	workflowResumeClaimHeartbeatHook = func() { refreshed <- struct{}{} }

	errCh := make(chan error, 1)
	go func() {
		errCh <- prepareWorkflowResumeExecution(ctx, WorkflowControllerBuild{Controller: ctrl}, repo, runID, true, io.Discard)
	}()

	// Wait for the first heartbeat, then forcefully take the claim away.
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not refresh within the test timeout")
	}
	if err := repo.TakeoverRunClaim(ctx, runID, "third-party"); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("prepareWorkflowResumeExecution() error = nil, want join cancelled after claim loss")
		}
		if !errors.Is(err, errResumeHandoffClaimLost) {
			t.Fatalf("prepareWorkflowResumeExecution() error = %v, want errResumeHandoffClaimLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prepareWorkflowResumeExecution did not return after the claim was lost")
	}
}
