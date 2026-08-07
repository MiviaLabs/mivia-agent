package localengine_test

import (
	"context"
	"encoding/json"
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
