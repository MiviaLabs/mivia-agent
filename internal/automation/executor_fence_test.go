package automation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestRunOnceFencedCheckpointReturnsErrRunFenced proves a checkpoint
// that lost the fence is reported as ErrRunFenced, not as a RunFailed
// view the row does not carry. The fake rotates the row's claim token
// during the only step, so the checkpoint write after it is fenced out.
func TestRunOnceFencedCheckpointReturnsErrRunFenced(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	id := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	conv := newRecordingConversation()
	svc, err := New(root, db, &fakeExecSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var runID string
	conv.onSend = func() {
		runs := svc.Runs(id, 1)
		if len(runs) != 1 {
			t.Errorf("runs during step = %+v, want the one running row", runs)
			return
		}
		runID = runs[0].ID
		if err := db.UpdateAutomationRunClaimToken(context.Background(), runID, "rotated-by-test"); err != nil {
			t.Errorf("rotate claim token: %v", err)
		}
	}

	run, err := svc.RunOnce(context.Background(), id, ports.TriggerManual)
	if !errors.Is(err, ErrRunFenced) {
		t.Fatalf("RunOnce = (%+v, %v), want ErrRunFenced", run, err)
	}
	row, ok, gerr := db.GetAutomationRun(context.Background(), runID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, gerr)
	}
	if row.State != string(RunRunning) || row.StepIndex != 0 {
		t.Fatalf("fenced row = state %q step %d, want the untouched running row at step 0", row.State, row.StepIndex)
	}
}

// TestRunOnceFailedTerminalWriteReturnsDurableRow proves a terminal
// write that fails for a store reason returns the row as stored, not
// the state the writer wanted. The trigger allows the running and
// session writes, then fails the RunFailed write.
func TestRunOnceFailedTerminalWriteReturnsDurableRow(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	id := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	conv := newRecordingConversation()
	conv.failAt = 1
	svc, err := New(root, db, &fakeExecSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 2); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), id, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunRunning || run.Message != "" {
		t.Fatalf("RunOnce view = state %v message %q, want the durable running row with no message", run.State, run.Message)
	}
	row, ok, gerr := db.GetAutomationRun(context.Background(), run.ID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, gerr)
	}
	if row.State != string(RunRunning) {
		t.Fatalf("row state = %q, want running", row.State)
	}
}

// tripwireContext is a context that cancels itself on the first Err
// call after arm. The executor checks the run ctx once after a turn
// drains, so arm inside the fake's Send makes that check pass and
// cancels the ctx before the checkpoint write. Err is safe for
// concurrent use: context.WithTimeout watches Done from a goroutine.
type tripwireContext struct {
	context.Context
	mu      sync.Mutex
	armed   bool
	tripped bool
	done    chan struct{}
}

func newTripwireContext() *tripwireContext {
	return &tripwireContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *tripwireContext) arm() {
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
}

func (c *tripwireContext) Done() <-chan struct{} { return c.done }

func (c *tripwireContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tripped {
		return context.Canceled
	}
	if c.armed {
		c.tripped = true
		close(c.done)
	}
	return nil
}

// TestCheckpointYieldsToConcurrentInterrupt proves the mid-run
// checkpoint never resurrects a row a concurrent interrupt already
// closed out. The fake fires InterruptRunningAutomationRun directly
// (standing in for Close's interruptStuckRuns path) between step one's
// completed Send and runSteps' own following checkpoint write - the
// exact interleaving the checkpoint's earlier cancel-durability fix
// (context.WithoutCancel) made reachable. The row must end interrupted
// at step_index 0 (the state the concurrent write set), never resurrected
// to running at step_index 1, and step two must never be dispatched.
func TestCheckpointYieldsToConcurrentInterrupt(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	id := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "one"}, {Kind: StepPrompt, Prompt: "two"}}
	})
	conv := newRecordingConversation()
	svc, err := New(root, db, &fakeExecSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var runID string
	conv.onSend = func() {
		runs := svc.Runs(id, 1)
		if len(runs) != 1 {
			t.Errorf("runs during step = %+v, want the one running row", runs)
			return
		}
		runID = runs[0].ID
		ok, ierr := db.InterruptRunningAutomationRun(context.Background(), runID, time.Now().UTC().Format(time.RFC3339), "interrupted: concurrent shutdown")
		if ierr != nil || !ok {
			t.Errorf("InterruptRunningAutomationRun = (%v, %v), want (true, nil)", ok, ierr)
		}
	}

	run, err := svc.RunOnce(context.Background(), id, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunInterrupted {
		t.Fatalf("RunOnce view = state %v, want RunInterrupted", run.State)
	}
	if got := conv.sentTexts(); len(got) != 1 {
		t.Fatalf("sent = %v, want only step 1 before the concurrent interrupt stopped the run", got)
	}
	row, ok, gerr := db.GetAutomationRun(context.Background(), runID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, gerr)
	}
	if row.State != string(RunInterrupted) || row.StepIndex != 0 {
		t.Fatalf("row = state %q step %d, want interrupted at step 0 (never resurrected to running)", row.State, row.StepIndex)
	}
}

// TestCheckpointSurvivesCancelAfterTurn proves a cancel that lands
// between the turn drain and the checkpoint does not lose the completed
// step: the row advances to step 1 and the run ends RunInterrupted
// before step 2, so a resume does not repeat step 1.
func TestCheckpointSurvivesCancelAfterTurn(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	id := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "one"}, {Kind: StepPrompt, Prompt: "two"}}
	})
	conv := newRecordingConversation()
	svc, err := New(root, db, &fakeExecSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := newTripwireContext()
	conv.onSend = ctx.arm

	done := make(chan struct{})
	var run ports.Run
	var runErr error
	go func() {
		defer close(done)
		run, runErr = svc.RunOnce(ctx, id, ports.TriggerManual)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not return")
	}
	if runErr != nil {
		t.Fatalf("RunOnce: %v", runErr)
	}
	if run.State != ports.RunInterrupted {
		t.Fatalf("RunOnce view = state %v, want RunInterrupted", run.State)
	}
	if got := conv.sentTexts(); len(got) != 1 {
		t.Fatalf("sent = %v, want only step 1 before the cancel", got)
	}
	row, ok, gerr := db.GetAutomationRun(context.Background(), run.ID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, gerr)
	}
	if row.State != string(RunInterrupted) || row.StepIndex != 1 {
		t.Fatalf("row = state %q step %d, want interrupted at step 1", row.State, row.StepIndex)
	}
}
