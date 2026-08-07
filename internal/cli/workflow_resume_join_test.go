package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
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
	if err := executeWorkflowResume(run.RunID, root, configPath, true, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowResume(force) error = %v", err)
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

// newJoinResumeFixture builds the run, in-flight attempt, join-capable runner
// and controller for the resume-join scenario, wiring the resume boundary seams.
func newJoinResumeFixture(t *testing.T) (root string, repo workflowledger.Repository, run workflowledger.RunSnapshot, runner *workflowResumeJoinRunner, snapshot workflowledger.Snapshot, ctrl *controller.LinearController) {
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
	// Seed a RUNNING attempt whose coordinator child already completed.
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: run.RunID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-rec", TaskID: "task-rec",
	}); err != nil {
		t.Fatal(err)
	}
	runner = &workflowResumeJoinRunner{children: map[string]controller.AgentStepResult{
		"task-rec": {
			CoordinatorRunID: "coord-rec", TaskID: "task-rec",
			Output: json.RawMessage(`{"ok":true}`), EvidenceJSON: []byte(`[]`), Status: "completed",
		},
	}}
	steps := map[string]controller.StepRuntime{
		"one": {Agent: agents.ResolvedAgent{Name: "one"}, ProviderName: "openrouter", Model: "test/model"},
		"two": {Agent: agents.ResolvedAgent{Name: "two"}, ProviderName: "openrouter", Model: "test/model"},
	}
	ctrl, err = controller.NewLinearController(repo, runner, compiled, steps, map[string]any{"task": "test"}, run.RunID, rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	originalOpen := workflowResumeOpenStore
	originalBuild := workflowResumeBuild
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeBuild = originalBuild
	})
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		return workflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(snapshot.Inputs)},
		}, nil
	}
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
	originalOpen := workflowResumeOpenStore
	originalBuild := workflowResumeBuild
	originalAdmission := workflowResumeSetAdmission
	originalForce := workflowResumeSetForce
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeBuild = originalBuild
		workflowResumeSetAdmission = originalAdmission
		workflowResumeSetForce = originalForce
	})
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		return workflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, nil
	}
	workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
	workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
	err = executeWorkflowResume(run.RunID, root, filepath.Join(root, "config.toml"), true, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot join") {
		t.Fatalf("executeWorkflowResume() error = %v, want nil-controller join refusal", err)
	}
}
