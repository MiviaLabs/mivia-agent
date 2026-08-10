package localengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// interruptGateRepository pauses the first Interrupt-status attempt completion
// after arm, so a test can inject a concurrent step completion while Interrupt
// marks open attempts interrupted.
type interruptGateRepository struct {
	workflowledger.Repository
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

var errInterruptedAttemptPersist = errors.New("injected interrupted attempt persistence failure")

// interruptedAttemptFailRepository fails the persistence write that Interrupt
// uses to mark an open attempt as interrupted.
type interruptedAttemptFailRepository struct {
	workflowledger.Repository
}

func (r *interruptedAttemptFailRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	if outcome.Status == workflowledger.AttemptStatusInterrupted {
		return errInterruptedAttemptPersist
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

// releaseAfterStopRepository records whether Interrupt releases the claim
// before the active controller stops.
type releaseAfterStopRepository struct {
	workflowledger.Repository
	stopped            <-chan struct{}
	releasedBeforeStop atomic.Bool
}

func (r *releaseAfterStopRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	select {
	case <-r.stopped:
	default:
		r.releasedBeforeStop.Store(true)
	}
	return r.Repository.ReleaseRun(ctx, runID, holder)
}

// releaseGateRepository pauses the claim release after Interrupt stops the
// controller, so a test can attempt a concurrent force-resume while Interrupt
// is still in progress (its interrupting gate is armed).
type releaseGateRepository struct {
	workflowledger.Repository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *releaseGateRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Repository.ReleaseRun(ctx, runID, holder)
}

// interruptBlockingRunner reports when its step starts and when its context
// ends. It proves that Interrupt joins the active controller on an error path.
type interruptBlockingRunner struct {
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *interruptBlockingRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.once.Do(func() { close(r.started) })
	defer close(r.stopped)
	select {
	case <-ctx.Done():
		return controller.AgentStepResult{}, ctx.Err()
	case <-r.release:
		return controller.AgentStepResult{CoordinatorRunID: "coord-" + req.StepID, TaskID: req.TaskID, Output: json.RawMessage(`{"ok":true}`), EvidenceJSON: []byte(`[]`)}, nil
	}
}

func (g *interruptGateRepository) arm() { g.armed.Store(true) }

func (g *interruptGateRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	if g.armed.Load() && outcome.Status == workflowledger.AttemptStatusInterrupted {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

// TestInterruptFencesBeforeMarkingAttempts pins the Interrupt order: the
// abandon fence arms before open attempts become interrupted. A step that
// completes while Interrupt holds the attempt write must not settle the run
// terminal; the run stays running and resumable. Regression: Interrupt marked
// attempts before arming the fence, so the dying controller hit a version
// conflict and settled the run to failed through the unfenced window.
func runInterruptFenceCycle(t *testing.T, cycle int) {
	t.Helper()
	{
		engine, gate, block, entered := newInterruptFenceEngine(t)
		started := startInterruptFenceRun(t, engine, entered, cycle)

		interruptErr := make(chan error, 1)
		go func() { interruptErr <- engine.Interrupt(started.RunID) }()
		select {
		case <-gate.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("Interrupt did not reach the attempt write")
		}
		// Unblock the in-flight step while Interrupt holds the attempt write.
		// The fence is already armed at this point; the dying controller's
		// terminal write must be rejected, so no sleep is needed.
		close(block)
		close(gate.release)
		if err := <-interruptErr; err != nil {
			t.Fatalf("cycle %d: Interrupt: %v", cycle, err)
		}

		run := getRunInterrupt(t, gate, started.RunID, cycle)
		if workflowledger.IsTerminalRunStatus(run.Status) {
			t.Fatalf("cycle %d: run status = %s after Interrupt, want non-terminal", cycle, run.Status)
		}
		resumeAndSucceed(t, engine, gate, started.RunID, cycle)
	}
}

func newInterruptFenceEngine(t *testing.T) (*localengine.Engine, *interruptGateRepository, chan struct{}, chan struct{}) {
	t.Helper()
	root := writeTwoStepWorkspace(t)
	gate := &interruptGateRepository{
		Repository: workflowledger.NewMemoryRepository(),
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: gate,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output:     json.RawMessage(`{"ok":true}`),
				BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					gate.arm()
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
	}
	return engine, gate, block, entered
}

func startInterruptFenceRun(t *testing.T, engine *localengine.Engine, entered chan struct{}, cycle int) agenttools.StartResult {
	t.Helper()
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("cycle %d: start: %v", cycle, err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	return started
}

func getRunInterrupt(t *testing.T, repo workflowledger.Repository, runID string, cycle int) workflowledger.RunSnapshot {
	t.Helper()
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("cycle %d: get run: %v", cycle, err)
	}
	return run
}

func resumeAndSucceed(t *testing.T, engine *localengine.Engine, gate *interruptGateRepository, runID string, cycle int) {
	t.Helper()
	resumed, err := engine.Start(context.Background(), agenttools.StartRequest{
		RunID: runID, Resume: true, Force: true,
	})
	if err != nil {
		t.Fatalf("cycle %d: resume: %v", cycle, err)
	}
	if !resumed.Resumed {
		t.Fatalf("cycle %d: resume result = %+v, want Resumed", cycle, resumed)
	}
	waitRun(t, engine, runID)
	run := getRunInterrupt(t, gate, runID, cycle)
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("cycle %d: run status = %s after resume, want succeeded", cycle, run.Status)
	}
}

func TestInterruptFencesBeforeMarkingAttempts(t *testing.T) {
	for cycle := 0; cycle < 2; cycle++ {
		runInterruptFenceCycle(t, cycle)
	}
}

// TestInterruptStopsControllerAfterAttemptPersistenceError keeps the active
// controller handle until Interrupt cancels and joins it. Regression: Interrupt
// removed the handle, then returned an attempt-persistence error without
// canceling the controller.
func TestInterruptStopsControllerAfterAttemptPersistenceError(t *testing.T) {
	runner := &interruptBlockingRunner{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	engine := &localengine.Engine{
		WorkspaceRoot: writeTwoStepWorkspace(t),
		Repo:          &interruptedAttemptFailRepository{Repository: workflowledger.NewMemoryRepository()},
		NewRunner: func() controller.AgentStepRunner {
			return runner
		},
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); !errors.Is(err, errInterruptedAttemptPersist) {
		t.Fatalf("Interrupt error = %v, want interrupted-attempt persistence error", err)
	}
	select {
	case <-runner.stopped:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Interrupt returned before it stopped the active controller")
	}
}

// TestInterruptStopsControllerBeforeClaimRelease prevents another engine from
// resuming a run while the interrupted controller can still do external work.
// Regression: Interrupt cleared the claim before it canceled and joined the
// controller.
func TestInterruptStopsControllerBeforeClaimRelease(t *testing.T) {
	runner := &interruptBlockingRunner{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	repo := &releaseAfterStopRepository{
		Repository: workflowledger.NewMemoryRepository(),
		stopped:    runner.stopped,
	}
	engine := &localengine.Engine{
		WorkspaceRoot: writeTwoStepWorkspace(t),
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return runner
		},
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if repo.releasedBeforeStop.Load() {
		t.Fatal("Interrupt released the run claim before it stopped the active controller")
	}
}

// TestInterruptBlocksResumeDuringClaimCleanup keeps a new controller from
// starting while Interrupt is still in progress: the interrupting gate is armed
// for the whole cleanup, so a concurrent force-resume is refused until
// Interrupt finishes.
func TestInterruptBlocksResumeDuringClaimCleanup(t *testing.T) {
	runner := &interruptBlockingRunner{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	repo := &releaseGateRepository{
		Repository: workflowledger.NewMemoryRepository(),
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	defer close(runner.release)
	var runners atomic.Int32
	engine := &localengine.Engine{
		WorkspaceRoot: writeTwoStepWorkspace(t),
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			if runners.Add(1) == 1 {
				return runner
			}
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- engine.Interrupt(started.RunID) }()
	select {
	case <-repo.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Interrupt did not start claim cleanup")
	}
	_, err = engine.Start(context.Background(), agenttools.StartRequest{
		RunID:  started.RunID,
		Resume: true,
		Force:  true,
	})
	if err == nil {
		t.Fatal("force resume succeeded while Interrupt cleared the old claim")
	}
	close(repo.release)
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

// TestInterruptRefusesForeignActiveRun prevents one engine from changing the
// open attempt of a controller that another engine owns.
func TestInterruptRefusesForeignActiveRun(t *testing.T) {
	runner := &interruptBlockingRunner{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	repo := workflowledger.NewMemoryRepository()
	var runners atomic.Int32
	owner := &localengine.Engine{
		WorkspaceRoot: writeTwoStepWorkspace(t),
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			if runners.Add(1) == 1 {
				return runner
			}
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	started, err := owner.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	foreign := &localengine.Engine{Repo: repo}
	if err := foreign.Interrupt(started.RunID); err == nil {
		t.Fatal("foreign Interrupt succeeded for an active run")
	}
	attempts, err := repo.ListStepAttempts(context.Background(), started.RunID)
	if err != nil {
		t.Fatalf("ListStepAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("attempts = %+v, want one running attempt", attempts)
	}
	if err := owner.Interrupt(started.RunID); err != nil {
		t.Fatalf("owner Interrupt: %v", err)
	}
}

// foreignClaimOnReleaseRepository simulates a foreign resume winning the run
// claim in the release-to-cleanup window: immediately after the interrupted
// controller's holder-scoped release frees the claim, it acquires the claim for
// a foreign holder, exactly the interleaving a concurrent resume on another
// host can hit between the controller's ReleaseRun and Interrupt's cleanup.
type foreignClaimOnReleaseRepository struct {
	workflowledger.Repository
	foreignClaimed bool
}

func (r *foreignClaimOnReleaseRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	err := r.Repository.ReleaseRun(ctx, runID, holder)
	// Only simulate the takeover once, and only when this release actually
	// freed the claim (the controller's own holder-scoped release).
	if err == nil && !r.foreignClaimed {
		r.foreignClaimed = true
		if cerr := r.Repository.ClaimRun(ctx, runID, "foreign-resumer"); cerr != nil {
			panic("foreign claim after controller release: " + cerr.Error())
		}
	}
	return err
}

// TestInterruptDoesNotStripForeignResumeClaim: Interrupt's claim cleanup must
// release by the interrupted controller's OWN holder, never delete the claim
// row blind. Regression: Interrupt called ClearRunClaim after <-active.done,
// so a claim acquired by a foreign resume in the release-to-cleanup window was
// stripped and the foreign executor's fenced writes failed (or a third
// executor double-ran the step).
func TestInterruptDoesNotStripForeignResumeClaim(t *testing.T) {
	runner := &interruptBlockingRunner{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	base := workflowledger.NewMemoryRepository()
	repo := &foreignClaimOnReleaseRepository{Repository: base}
	engine := &localengine.Engine{
		WorkspaceRoot: writeTwoStepWorkspace(t),
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return runner
		},
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if !repo.foreignClaimed {
		t.Fatal("test setup did not reach the foreign-claim window")
	}
	// The foreign resume's claim must survive Interrupt's cleanup.
	if err := base.ClaimRun(context.Background(), started.RunID, "third-executor"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after Interrupt = %v, want ErrClaimHeld (the foreign claim must survive)", err)
	}
}
