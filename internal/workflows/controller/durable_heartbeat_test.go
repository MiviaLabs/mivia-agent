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
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// countHeartbeatEvents returns how many wf_attempt_heartbeat events the run's
// audit trail holds for one attempt.
func countHeartbeatEvents(t *testing.T, repo workflowledger.Repository, runID, attemptID string) int {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), runID, 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.Kind == "wf_attempt_heartbeat" && containsAttempt(ev.Summary, attemptID) {
			n++
		}
	}
	return n
}

func containsAttempt(summary, attemptID string) bool {
	// The summary is "attempt <id> heartbeat at <time>". Anchor the id with
	// surrounding spaces so wfa-first-1 never matches wfa-first-10.
	return strings.Contains(summary, " "+attemptID+" ")
}

// TestDurableHeartbeatThrottleLimitsWrites pins the throttle contract: the
// first heartbeat for an attempt always persists, later ones only after the
// interval has elapsed, and distinct attempts are throttled independently.
func TestDurableHeartbeatThrottleLimitsWrites(t *testing.T) {
	old := durableHeartbeatInterval
	durableHeartbeatInterval = 15 * time.Second
	t.Cleanup(func() { durableHeartbeatInterval = old })

	th := newDurableHeartbeatThrottle()
	base := time.Now()
	if !th.shouldPersist("att-1", base) {
		t.Fatal("first heartbeat for an attempt must persist")
	}
	if th.shouldPersist("att-1", base.Add(5*time.Second)) {
		t.Fatal("heartbeat inside the interval must be throttled")
	}
	if !th.shouldPersist("att-1", base.Add(16*time.Second)) {
		t.Fatal("heartbeat after the interval must persist")
	}
	// A different attempt is throttled independently of att-1.
	if !th.shouldPersist("att-2", base) {
		t.Fatal("a second attempt must not be throttled by the first attempt's write")
	}
}

// heartbeatEmittingRunner blocks until release and emits a step heartbeat
// every few milliseconds through the controller's progress sink, mirroring
// the production CoordinatorRunner join watchdog. The emitted identity comes
// from the step request, exactly like joinWithCancellation's wrapper.
type heartbeatEmittingRunner struct {
	started chan AgentStepRequest
	release chan struct{}
	emit    func(ProgressEvent)
}

func (r *heartbeatEmittingRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.started <- req
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.release:
			return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Output: json.RawMessage(`{"ok":true}`)}, nil
		case <-ticker.C:
			if r.emit != nil {
				r.emit(ProgressEvent{
					Kind: ProgressStepHeartbeat, StepID: req.StepID, AttemptNo: req.AttemptNo,
					TaskID: req.TaskID, CoordinatorRunID: req.CoordinatorRunID, Detail: "running",
				})
			}
		}
	}
}

// TestAgentStepDurableHeartbeatWhileRunning: a RUNNING agent step whose join
// emits step heartbeats must persist DURABLE wf_attempt_heartbeat events for
// its admitted attempt while it runs (at least two across the throttle
// interval), and the step must still complete successfully.
func TestAgentStepDurableHeartbeatWhileRunning(t *testing.T) {
	old := durableHeartbeatInterval
	durableHeartbeatInterval = 15 * time.Millisecond
	t.Cleanup(func() { durableHeartbeatInterval = old })

	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &heartbeatEmittingRunner{started: make(chan AgentStepRequest, 1), release: make(chan struct{})}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-durable-heartbeat-agent", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	// Wire the runner's emitter into the controller progress sink exactly like
	// the CLI's newWorkflowController does for the production runner.
	runner.emit = ctrl.EmitProgress
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, _, runErr := ctrl.Advance(context.Background()); done <- runErr }()
	<-runner.started

	// The join is live and the attempt wfa-first-1 is running: durable
	// heartbeats must land in the ledger while the step is still running.
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.After(5 * time.Second)
	for {
		if countHeartbeatEvents(t, repo, ctrl.RunID, "wfa-first-1") >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("agent step produced fewer than 2 durable heartbeat events while running")
		case <-poll.C:
		}
	}

	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("advance failed after durable heartbeats: %v", err)
	}
	attempt, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, "wfa-first-1")
	if err != nil {
		t.Fatalf("GetStepAttempt: %v", err)
	}
	if attempt.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded", attempt.Status)
	}
	if attempt.LastHeartbeatAt.IsZero() {
		t.Fatal("attempt LastHeartbeatAt is zero; durable heartbeat did not reach the projection")
	}
}

// TestDurableHeartbeatWriteErrorNeverFailsStep: a ledger write error on the
// durable heartbeat path is best-effort — the step must still succeed and the
// in-memory fast path must stay untouched.
func TestDurableHeartbeatWriteErrorNeverFailsStep(t *testing.T) {
	old := durableHeartbeatInterval
	durableHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { durableHeartbeatInterval = old })

	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	repo := &heartbeatFailingRepository{Repository: base}
	runner := &heartbeatEmittingRunner{started: make(chan AgentStepRequest, 1), release: make(chan struct{})}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-durable-heartbeat-fail", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	runner.emit = ctrl.EmitProgress
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, runErr := ctrl.Advance(context.Background()); done <- runErr }()
	<-runner.started
	// Let a few failing heartbeat writes happen.
	select {
	case <-time.After(80 * time.Millisecond):
	case <-done:
		t.Fatal("advance ended before the heartbeat window")
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("step failed on a durable heartbeat write error: %v", err)
	}
}

// heartbeatFailingRepository fails every durable heartbeat write.
type heartbeatFailingRepository struct {
	workflowledger.Repository
}

func (r *heartbeatFailingRepository) SetStepAttemptHeartbeat(context.Context, string, string, time.Time) error {
	return errors.New("injected ledger write failure")
}

// blockingVerifierProfile blocks until release (or ctx cancel), then returns
// the fixed result. It models a LONG-RUNNING synchronous host verifier.
type blockingVerifierProfile struct {
	name    string
	started chan struct{}
	release chan struct{}
	result  verifier.Result
}

func (p *blockingVerifierProfile) Name() string { return p.name }

func (p *blockingVerifierProfile) Verify(ctx context.Context, _ verifier.Request) (verifier.Result, error) {
	close(p.started)
	select {
	case <-p.release:
		return p.result, nil
	case <-ctx.Done():
		return verifier.Result{}, ctx.Err()
	}
}

// TestEvidenceGateDurableHeartbeatWhileRunning: a RUNNING evidence gate whose
// synchronous verifier takes time must persist DURABLE wf_attempt_heartbeat
// events for its gate attempt while the verifier runs (at least two across
// the throttle interval), then settle succeeded once the verifier returns.
func TestEvidenceGateDurableHeartbeatWhileRunning(t *testing.T) {
	old := durableHeartbeatInterval
	durableHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { durableHeartbeatInterval = old })

	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-durable-heartbeat", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "slow-check"}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	gate := &blockingVerifierProfile{
		name: "slow-check", started: make(chan struct{}), release: make(chan struct{}),
		result: verifier.Result{Status: "passed"},
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(gate); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-durable-heartbeat-gate", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, _, runErr := ctrl.Advance(context.Background()); done <- runErr }()
	<-gate.started

	// The verifier is running: durable heartbeats for wfa-verify-1 must land
	// in the ledger before it returns.
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.After(5 * time.Second)
	for {
		if countHeartbeatEvents(t, repo, ctrl.RunID, "wfa-verify-1") >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("evidence gate produced fewer than 2 durable heartbeat events while the verifier ran")
		case <-poll.C:
		}
	}

	close(gate.release)
	if err := <-done; err != nil {
		t.Fatalf("advance failed after gate heartbeats: %v", err)
	}
	attempt, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, "wfa-verify-1")
	if err != nil {
		t.Fatalf("GetStepAttempt: %v", err)
	}
	if attempt.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("gate attempt status = %q, want succeeded", attempt.Status)
	}
	if attempt.LastHeartbeatAt.IsZero() {
		t.Fatal("gate attempt LastHeartbeatAt is zero; durable heartbeat did not reach the projection")
	}
}
