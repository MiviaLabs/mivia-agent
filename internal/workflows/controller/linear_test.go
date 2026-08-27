package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type linearRunner struct {
	mu      sync.Mutex
	calls   []AgentStepRequest
	outputs map[string]json.RawMessage
}

func TestLinearControllerStartPersistsAdmissionAndOneDeadline(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Limits.MaxDurationSeconds = 30
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "build"}, "wfr-admission", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	admittedAt := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	if err := ctrl.SetAdmission(Admission{BaseRef: "main", BaseCommit: "abc123", OriginBaseCommit: "origin-abc123", WorktreeName: "workflow-1", InputDigest: "inputs-digest"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetTimeSource(func() time.Time { return admittedAt }); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseRef != "main" || got.BaseCommit != "abc123" || got.OriginBaseCommit != "origin-abc123" || got.WorktreeName != "workflow-1" {
		t.Fatalf("admission = %+v", got)
	}
	if got.SnapshotDigest != workflowledger.SnapshotDigest([]byte("snapshot")) || got.InputDigest != "inputs-digest" {
		t.Fatalf("digests = %q/%q", got.SnapshotDigest, got.InputDigest)
	}
	wantDeadline := admittedAt.Add(30 * time.Second)
	if got.DeadlineAt == nil || !got.DeadlineAt.Equal(wantDeadline) {
		t.Fatalf("deadline = %v, want %v", got.DeadlineAt, wantDeadline)
	}
	if !got.StartedAt.Equal(admittedAt) {
		t.Fatalf("started at = %v, want %v", got.StartedAt, admittedAt)
	}
	if err := ctrl.SetTimeSource(time.Now); err == nil {
		t.Fatal("clock changed after admission")
	}
}

func TestLinearControllerDuplicateStartRequiresStoredDeadline(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Limits.MaxDurationSeconds = 30
	repo := workflowledger.NewMemoryRepository()
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	first, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-deadline-duplicate", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, first.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := start.Add(30 * time.Second)
	if err := second.SetAdmission(Admission{DeadlineAt: &deadline}); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	third, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, first.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	changed := deadline.Add(time.Second)
	if err := third.SetAdmission(Admission{DeadlineAt: &changed}); err != nil {
		t.Fatal(err)
	}
	if err := third.Start(context.Background()); err == nil {
		t.Fatal("changed deadline was accepted")
	}
}

func TestLinearControllerDuplicateStartRejectsChangedAdmission(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	newController := func(commit string) *LinearController {
		ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-admission-duplicate", []byte("same"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ctrl.SetAdmission(Admission{BaseRef: "main", BaseCommit: commit, WorktreeName: "workflow-1", InputDigest: "input"}); err != nil {
			t.Fatal(err)
		}
		return ctrl
	}
	if err := newController("first").Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := newController("second").Start(context.Background()); err == nil {
		t.Fatal("changed admission was accepted")
	}
}

func TestLinearControllerUsesStoredExpiredDeadline(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Limits.MaxDurationSeconds = 10
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{}
	ctrl, err := NewLinearController(repo, runner, wf, nil, nil, "wfr-expired", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	if err := ctrl.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctrl.now = func() time.Time { return start.Add(time.Hour) }
	got, err := ctrl.Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("run = %+v, err = %v", got, err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("expired run dispatched work")
	}
}

func TestLinearControllerReconcilesReservedSuccessRoute(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{}
	ctrl, err := NewLinearController(repo, runner, wf, nil, nil, "wfr-route-success", []byte("snapshot"))
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
	if err := repo.CompleteStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusSucceeded, ToStepID: "success"}); err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("advance = %+v, done=%v, err=%v", got, done, err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("terminal route dispatched work")
	}
}

func TestLinearControllerRetriesOnlyInterruptedAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-interrupted", []byte("snapshot"))
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
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 || attempts[1].AttemptNo != 2 {
		t.Fatalf("attempts = %+v, err=%v", attempts, err)
	}
	if len(runner.calls) != 1 || runner.calls[0].CoordinatorRunID == "" || runner.calls[0].TaskID == "" {
		t.Fatalf("calls = %+v", runner.calls)
	}
}

func TestLinearControllerPropagatesStepSkill(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Steps[0].Skill = "workflow-delivery"
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-step-skill", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].Skill != "workflow-delivery" {
		t.Fatalf("step request = %+v, want workflow-delivery skill", runner.calls)
	}
}

type blockingIdentityRunner struct {
	started chan AgentStepRequest
	release chan struct{}
}

type claimCountingRepository struct {
	workflowledger.Repository
	claims  atomic.Int32
	renewed chan struct{}
	once    sync.Once
}

func (r *claimCountingRepository) RefreshRunClaim(ctx context.Context, runID, holder string) error {
	if r.claims.Add(1) >= 2 && r.renewed != nil {
		r.once.Do(func() { close(r.renewed) })
	}
	return r.Repository.RefreshRunClaim(ctx, runID, holder)
}

func TestLinearControllerRenewsClaimDuringStep(t *testing.T) {
	old := claimHeartbeatInterval
	claimHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { claimHeartbeatInterval = old })

	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	repo := &claimCountingRepository{Repository: base, renewed: make(chan struct{})}
	runner := &blockingIdentityRunner{started: make(chan AgentStepRequest, 1), release: make(chan struct{})}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{"first": {Agent: agents.ResolvedAgent{Name: "one"}}}, map[string]any{"task": "build"}, "wfr-heartbeat", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, runErr := ctrl.Advance(context.Background()); done <- runErr }()
	<-runner.started
	select {
	case <-repo.renewed:
	case <-time.After(time.Second):
		close(runner.release)
		<-done
		t.Fatalf("ClaimRun calls = %d, want initial claim plus heartbeat", repo.claims.Load())
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func (r *blockingIdentityRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.started <- req
	<-r.release
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Output: json.RawMessage(`{"ok":true}`)}, nil
}

func TestConcurrentControllersCreateOneAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &blockingIdentityRunner{started: make(chan AgentStepRequest, 1), release: make(chan struct{})}
	newController := func() *LinearController {
		ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{"first": {Agent: agents.ResolvedAgent{Name: "one"}}}, map[string]any{"task": "build"}, "wfr-concurrent", []byte("snapshot"))
		if err != nil {
			t.Fatal(err)
		}
		return ctrl
	}
	first, second := newController(), newController()
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { _, _, err := first.Advance(context.Background()); firstDone <- err }()
	request := <-runner.started
	if request.CoordinatorRunID == "" || request.TaskID == "" {
		t.Fatalf("identity = %+v", request)
	}
	if _, _, err := second.Advance(context.Background()); err == nil {
		t.Fatal("second controller acquired a held run")
	}
	close(runner.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), first.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%d, err=%v", len(attempts), err)
	}
}

func TestLinearControllerPersistsChildIdentityBeforeDispatch(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &identityCheckingRunner{repo: repo, runID: "wfr-child-identity", output: json.RawMessage(`{"ok":true}`)}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one", MaxTokens: intp(99)}, ProviderName: "provider-a", Model: "model-a"},
	}, map[string]any{"task": "build"}, runner.runID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err == nil {
		t.Fatal("advance without start succeeded")
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.seen.CoordinatorRunID == "" || runner.seen.TaskID == "" {
		t.Fatalf("request identity = %+v", runner.seen)
	}
	if runner.seen.ProviderName != "provider-a" || runner.seen.Model != "model-a" || runner.seen.Budget != 0 {
		t.Fatalf("request routing or budget = %+v", runner.seen)
	}
}

type identityCheckingRunner struct {
	repo   workflowledger.Repository
	runID  string
	seen   AgentStepRequest
	output json.RawMessage
}

func (r *identityCheckingRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	attempts, err := r.repo.ListStepAttempts(ctx, r.runID)
	if err != nil || len(attempts) != 1 {
		return AgentStepResult{}, fmt.Errorf("load admitted attempt: %w", err)
	}
	if attempts[0].CoordinatorRunID != req.CoordinatorRunID || attempts[0].TaskID != req.TaskID {
		return AgentStepResult{}, errors.New("child identity was not persisted")
	}
	r.seen = req
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Output: r.output}, nil
}

func intp(value int) *int { return &value }

type canceledLinearRunner struct{}

func (canceledLinearRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, context.Canceled
}

func (r *linearRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	r.mu.Unlock()
	return AgentStepResult{CoordinatorRunID: "coord-" + req.StepID, TaskID: req.TaskID, Output: r.outputs[req.StepID], EvidenceJSON: []byte(`[]`)}, nil
}

func linearWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "linear", InitialStep: "first",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}, "secret": {Type: "string"}},
		Steps: []definition.Step{
			{ID: "first", Kind: "agent", Agent: "one", Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "second", Kind: "agent", Agent: "two", Context: []definition.ContextBinding{{From: "steps.first.output", As: "first"}}},
		},
		Transitions: []definition.Transition{
			{From: "first", To: "second", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "second", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestLinearControllerRunsTwoStepsAndPersistsChildReferences(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`), "second": json.RawMessage(`{"done":true}`)}}
	repo := workflowledger.NewMemoryRepository()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("linear"), DefinitionDigest: wf.Digest})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}, Digest: "sha256:one"},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}, Digest: "sha256:two"},
	}
	ctrl, err := NewLinearController(repo, runner, wf, steps, map[string]any{"task": "build"}, "wfr-linear", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err=%v", len(attempts), err)
	}
	for _, attempt := range attempts {
		if attempt.CoordinatorRunID == "" || attempt.TaskID == "" || attempt.OutputRef == "" {
			t.Fatalf("attempt has incomplete child identity or output: %+v", attempt)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 || runner.calls[0].Inputs["secret"] != nil || runner.calls[1].Inputs["secret"] != nil || runner.calls[1].Evidence["first"] == nil {
		t.Fatalf("calls = %+v", runner.calls)
	}
}

func TestLinearControllerAcceptsLoopTransitions(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "looped", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, compiled, nil, nil, "wfr-loop-ok", []byte("snapshot"))
	if err != nil {
		t.Fatalf("loop transitions rejected: %v", err)
	}
}

func TestLinearControllerRejectsDuplicateSnapshot(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{}`), "second": json.RawMessage(`{}`)}}
	first, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{"first": {Agent: agents.ResolvedAgent{Name: "one"}}, "second": {Agent: agents.ResolvedAgent{Name: "two"}}}, map[string]any{"task": "a"}, "wfr-duplicate", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewLinearController(repo, runner, wf, nil, map[string]any{"task": "b"}, "wfr-duplicate", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err == nil {
		t.Fatal("different duplicate snapshot was accepted")
	}
}

func TestLinearControllerResumesRecordedAttemptWithoutNewAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	repo := workflowledger.NewMemoryRepository()
	snapshot := []byte("resume-snapshot")
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-resume", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// A RUNNING attempt is a crash artifact (only a crashed or force-replaced
	// executor leaves one). This runner has NO join capability, so resume
	// falls back to the pre-join behavior: the stale attempt is marked
	// interrupted and a fresh attempt No+1 is admitted with a NEW identity, so
	// the step's agent work is not double-recorded under one attempt while the
	// old executor's fenced writes are discarded. (A join-capable runner would
	// instead JOIN the recorded coordinator run — see
	// TestLinearControllerJoinsInFlightAttemptOnResume.)
	if err := repo.CreateStepAttempt(context.Background(), workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-existing", TaskID: "task-existing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	if runner.calls[0].CoordinatorRunID == "coord-existing" || runner.calls[0].TaskID == "task-existing" {
		t.Fatalf("resume re-executed the stale attempt identity: %+v", runner.calls[0])
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err=%v, want 2 (interrupted stale + fresh)", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusInterrupted || attempts[0].AttemptNo != 1 {
		t.Fatalf("stale attempt = %+v, want interrupted No 1", attempts[0])
	}
	if attempts[1].AttemptNo != 2 {
		t.Fatalf("fresh attempt = %+v, want No 2", attempts[1])
	}
}

// joinAwareRunner simulates a coordinator whose previously dispatched children
// can be JOINED by their recorded identity: JoinStep reports the child's
// terminal outcome without dispatching anything, while RunStep records any
// fresh dispatch. A missing child reports joined=false (nothing to join).
type joinAwareRunner struct {
	mu       sync.Mutex
	joined   []AgentStepRequest
	dispatch []AgentStepRequest
	children map[string]AgentStepResult // by TaskID
}

func (r *joinAwareRunner) JoinStep(_ context.Context, req AgentStepRequest) (AgentStepResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.joined = append(r.joined, req)
	result, ok := r.children[req.TaskID]
	if !ok {
		return AgentStepResult{}, false, nil
	}
	// Mirror the production runner: a child that did not complete carries an
	// error alongside its terminal status.
	if result.Status != "" && result.Status != "completed" {
		return result, true, fmt.Errorf("child %s reported %s", req.TaskID, result.Status)
	}
	return result, true, nil
}

func (r *joinAwareRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatch = append(r.dispatch, req)
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, EvidenceJSON: []byte(`[]`)}, nil
}

// TestLinearControllerJoinsInFlightAttemptOnResume: on resume, an in-flight
// attempt whose recorded coordinator run already completed MUST be joined —
// the attempt completes with the child's outcome and route and the step is NOT
// re-executed (no fresh CoordinatorRunID/TaskID, no fresh attempt). This is the
// ledger contract (recovery.go: recorded attempts are JOINED, never
// re-dispatched).
func TestLinearControllerJoinsInFlightAttemptOnResume(t *testing.T) {
	ctx := context.Background()
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &joinAwareRunner{children: map[string]AgentStepResult{
		"task-existing": {
			CoordinatorRunID: "coord-existing", TaskID: "task-existing",
			Output: json.RawMessage(`{"ok":true}`), EvidenceJSON: []byte(`[]`), Status: "completed",
		},
	}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-join-resume", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// Seed a RUNNING attempt whose coordinator child already completed.
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-existing", TaskID: "task-existing",
	}); err != nil {
		t.Fatal(err)
	}
	// Resume: the recorded child must be JOINED, not re-dispatched.
	got, done, err := ctrl.Advance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatalf("advance = %+v, want non-terminal (routed to second)", got)
	}
	if got.ActiveStepID != "second" {
		t.Fatalf("active step = %q, want second", got.ActiveStepID)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.dispatch) != 0 {
		t.Fatalf("resume dispatched a fresh child: %+v", runner.dispatch)
	}
	if len(runner.joined) != 1 || runner.joined[0].CoordinatorRunID != "coord-existing" || runner.joined[0].TaskID != "task-existing" {
		t.Fatalf("joined requests = %+v, want the recorded identity", runner.joined)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, err=%v, want exactly 1 (no fresh attempt)", len(attempts), err)
	}
	attempt := attempts[0]
	if attempt.Status != workflowledger.AttemptStatusSucceeded || attempt.ToStepID != "second" {
		t.Fatalf("attempt = %+v, want succeeded routed to second", attempt)
	}
	if attempt.CoordinatorRunID != "coord-existing" || attempt.TaskID != "task-existing" {
		t.Fatalf("attempt identity changed after join: %+v", attempt)
	}
}

// TestLinearControllerJoinsFailedInFlightChild: a joined child that FAILED
// completes the attempt as failed with the on_failure route — it is recorded,
// not re-dispatched.
func TestLinearControllerJoinsFailedInFlightChild(t *testing.T) {
	ctx := context.Background()
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &joinAwareRunner{children: map[string]AgentStepResult{
		"task-existing": {
			CoordinatorRunID: "coord-existing", TaskID: "task-existing",
			Output: json.RawMessage(`{"partial":true}`), EvidenceJSON: []byte(`[]`), Status: "failed",
		},
	}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-join-failed", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-existing", TaskID: "task-existing",
	}); err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.Advance(ctx)
	if err == nil || !done || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("advance = %+v, done=%v, err=%v, want failed terminal", got, done, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.dispatch) != 0 {
		t.Fatalf("resume dispatched a fresh child after a failed join: %+v", runner.dispatch)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, err=%v, want exactly 1 (no fresh attempt)", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusFailed || attempts[0].ToStepID != "failure" {
		t.Fatalf("attempt = %+v, want failed routed to failure", attempts[0])
	}
	if attempts[0].CoordinatorRunID != "coord-existing" || attempts[0].TaskID != "task-existing" {
		t.Fatalf("attempt identity changed after failed join: %+v", attempts[0])
	}
}

// TestLinearControllerRedispatchWhenInFlightChildNeverRan: when the join shows
// the child never ran (nothing to join), the stale attempt is interrupted and
// a FRESH attempt with a new coordinator identity is dispatched — the
// fallback branch of the ledger contract.
func TestLinearControllerRedispatchWhenInFlightChildNeverRan(t *testing.T) {
	ctx := context.Background()
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &joinAwareRunner{}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-join-never-ran", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-never", TaskID: "task-never",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.joined) != 1 || runner.joined[0].TaskID != "task-never" {
		t.Fatalf("joined requests = %+v, want the recorded identity", runner.joined)
	}
	if len(runner.dispatch) != 1 {
		t.Fatalf("dispatch requests = %d, want 1 fresh dispatch", len(runner.dispatch))
	}
	if runner.dispatch[0].CoordinatorRunID == "coord-never" || runner.dispatch[0].TaskID == "task-never" {
		t.Fatalf("fresh dispatch reused the stale identity: %+v", runner.dispatch[0])
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err=%v, want 2 (interrupted stale + fresh)", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusInterrupted || attempts[0].AttemptNo != 1 {
		t.Fatalf("stale attempt = %+v, want interrupted No 1", attempts[0])
	}
	if attempts[1].AttemptNo != 2 || !workflowledger.IsTerminalAttemptStatus(attempts[1].Status) {
		t.Fatalf("fresh attempt = %+v, want terminal No 2", attempts[1])
	}
}

func TestLinearControllerPersistsCanceledAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, canceledLinearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-canceled", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, err=%v", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempt status = %q, want canceled", attempts[0].Status)
	}
}
