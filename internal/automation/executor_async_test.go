package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// gatedConversation is a ports.Conversation whose turn does not end
// until the test closes release. With honourCtx set, the turn also
// ends (channel closed, no TurnEnd) when the Send ctx is cancelled.
// Without it the turn ignores ctx, which models a step that cannot be
// interrupted.
type gatedConversation struct {
	release   chan struct{}
	honourCtx bool
	startOnce sync.Once
	started   chan struct{}
}

func newGatedConversation(honourCtx bool) *gatedConversation {
	return &gatedConversation{
		release:   make(chan struct{}),
		honourCtx: honourCtx,
		started:   make(chan struct{}),
	}
}

func (c *gatedConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.startOnce.Do(func() { close(c.started) })
	ch := make(chan uievent.Event, 1)
	var done <-chan struct{}
	if c.honourCtx {
		done = ctx.Done()
	}
	go func() {
		defer close(ch)
		select {
		case <-c.release:
			ch <- uievent.Event{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}}
		case <-done:
		}
	}()
	return &fakeTurnHandle{ch: ch}, nil
}

func (c *gatedConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *gatedConversation) History() []ports.Message             { return nil }
func (c *gatedConversation) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (c *gatedConversation) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *gatedConversation) Title() string                        { return "" }
func (c *gatedConversation) ID() string                           { return "gated-conv" }

// waitStarted fails the test when the first Send has not happened
// within one second.
func (c *gatedConversation) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start within 1s")
	}
}

// newAsyncService builds a Service over a fresh db and one seeded
// enabled automation, and closes the Service on cleanup.
func newAsyncService(t *testing.T, conv ports.Conversation, cfg Config, mutate func(*Spec)) (*Service, *storage.SQLite, string) {
	t.Helper()
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, mutate)
	svc, err := New(root, db, &fakeSpawner{conv: conv}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return svc, db, automationID
}

// drainSaveWithin drains h and fails the test when the handle does not
// close within timeout.
func drainSaveWithin(t *testing.T, h ports.SaveHandle, timeout time.Duration) ports.SaveEvent {
	t.Helper()
	out := make(chan ports.SaveEvent, 1)
	go func() { out <- drainSave(t, h) }()
	select {
	case ev := <-out:
		return ev
	case <-time.After(timeout):
		t.Fatalf("SaveHandle did not close within %v", timeout)
		return ports.SaveEvent{}
	}
}

// readUntilState reads h until a run in state arrives, and returns
// every run observed in order.
func readUntilState(t *testing.T, h ports.RunHandle, state ports.RunState, timeout time.Duration) []ports.Run {
	t.Helper()
	deadline := time.After(timeout)
	var seen []ports.Run
	for {
		select {
		case run, ok := <-h.Events():
			if !ok {
				t.Fatalf("Events closed before state %v; seen %+v", state, seen)
			}
			seen = append(seen, run)
			if run.State == state {
				return seen
			}
		case <-deadline:
			t.Fatalf("state %v not observed within %v; seen %+v", state, timeout, seen)
		}
	}
}

func watchOrFail(t *testing.T, svc *Service, automationID string) ports.RunHandle {
	t.Helper()
	h, err := svc.Watch(context.Background(), automationID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(h.Cancel)
	return h
}

func applyTrigger(t *testing.T, svc *Service, automationID string) ports.SaveHandle {
	t.Helper()
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.TriggerAutomation{ID: automationID})
	if err != nil {
		t.Fatalf("Apply(TriggerAutomation): %v", err)
	}
	return h
}

func TestApplyTriggerReturnsBeforeTurnCompletes(t *testing.T) {
	conv := newGatedConversation(true)
	svc, _, id := newAsyncService(t, conv, Config{}, nil)
	w := watchOrFail(t, svc, id)

	h := applyTrigger(t, svc, id)
	last := drainSaveWithin(t, h, time.Second)
	if last.State != ports.SaveSaved {
		t.Fatalf("final save event = %+v, want SaveSaved", last)
	}
	conv.waitStarted(t)
	close(conv.release)

	seen := readUntilState(t, w, ports.RunSucceeded, 2*time.Second)
	if seen[len(seen)-1].AutomationID != id {
		t.Fatalf("terminal run AutomationID = %q, want %q", seen[len(seen)-1].AutomationID, id)
	}
}

func stateRank(st ports.RunState) int {
	switch st {
	case ports.RunPending:
		return 0
	case ports.RunRunning:
		return 1
	default:
		return 2
	}
}

func TestWatchDeliversPendingRunningTerminalInOrder(t *testing.T) {
	conv := newGatedConversation(true)
	svc, _, id := newAsyncService(t, conv, Config{}, nil)
	w := watchOrFail(t, svc, id)

	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	// The run is blocked inside its first step: the first delivered
	// state must be non-terminal.
	select {
	case first := <-w.Events():
		if stateRank(first.State) == 2 {
			t.Fatalf("first event %+v is terminal while the step is still blocked", first)
		}
	case <-time.After(time.Second):
		t.Fatal("no run event while the step is blocked")
	}
	close(conv.release)
	seen := readUntilState(t, w, ports.RunSucceeded, 2*time.Second)
	prev := -1
	for _, run := range seen {
		if stateRank(run.State) < prev {
			t.Fatalf("out-of-order states: %+v", seen)
		}
		prev = stateRank(run.State)
	}
}

func TestWatchDeliversSkippedRow(t *testing.T) {
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx := context.Background()
	if _, ok, err := svc.admitFire(ctx, id); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}
	w := watchOrFail(t, svc, id)
	run, err := svc.RunOnce(ctx, id, ports.TriggerManual)
	if err != nil || run.State != ports.RunSkipped {
		t.Fatalf("RunOnce = (%+v, %v), want RunSkipped, nil", run, err)
	}
	seen := readUntilState(t, w, ports.RunSkipped, time.Second)
	if seen[0].ID != run.ID {
		t.Fatalf("watched skipped run ID = %q, want %q", seen[0].ID, run.ID)
	}
}

func TestWatchTerminalStateNeverDropped(t *testing.T) {
	steps := make([]Step, 10)
	for i := range steps {
		steps[i] = Step{Kind: StepPrompt, Prompt: "p"}
	}
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, func(sp *Spec) { sp.Steps = steps })
	w := watchOrFail(t, svc, id)

	// More than 8 state writes happen before anything is read.
	run, err := svc.RunOnce(context.Background(), id, ports.TriggerManual)
	if err != nil || run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce = (%+v, %v), want RunSucceeded", run, err)
	}
	seen := readUntilState(t, w, ports.RunSucceeded, time.Second)
	if len(seen) > 12 {
		t.Fatalf("expected coalescing, got %d events", len(seen))
	}
}

// TestCloseCancelsInFlightTurnRowIsInterrupted proves a shutdown
// cancellation is recorded as RunInterrupted, the one state a later
// resume admits, never as RunCancelled.
func TestCloseCancelsInFlightTurnRowIsInterrupted(t *testing.T) {
	conv := newGatedConversation(true)
	svc, db, id := newAsyncService(t, conv, Config{}, nil)
	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	runs := svc.Runs(id, 1)
	if len(runs) != 1 || runs[0].State != ports.RunInterrupted {
		t.Fatalf("runs after Close = %+v, want one RunInterrupted", runs)
	}
	row, ok, err := db.GetAutomationRun(context.Background(), runs[0].ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.StepIndex != 0 {
		t.Fatalf("StepIndex = %d, want 0 (not advanced)", row.StepIndex)
	}
	if _, ok, err := svc.admitFire(context.Background(), id); err != nil || !ok {
		t.Fatalf("admitFire after Close: ok=%v err=%v, want claim released", ok, err)
	}
}

// TestCancelRunMarksCancelledAndReleasesClaim proves an explicit
// CancelRun is the one path that yields RunCancelled.
func TestCancelRunMarksCancelledAndReleasesClaim(t *testing.T) {
	conv := newGatedConversation(true)
	svc, db, id := newAsyncService(t, conv, Config{}, nil)
	w := watchOrFail(t, svc, id)
	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	runs := svc.Runs(id, 1)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if err := svc.CancelRun(runs[0].ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	readUntilState(t, w, ports.RunCancelled, 2*time.Second)
	row, ok, err := db.GetAutomationRun(context.Background(), runs[0].ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.State != string(RunCancelled) || row.StepIndex != 0 {
		t.Fatalf("row = %+v, want cancelled at step 0", row)
	}
	svc.wg.Wait()
	if _, ok, err := svc.admitFire(context.Background(), id); err != nil || !ok {
		t.Fatalf("admitFire after CancelRun: ok=%v err=%v, want claim released", ok, err)
	}
}

// TestApplyCancelAutomationRunCancelsActiveRun proves Apply(CancelAutomationRun)
// cancels an active async run, persists and publishes RunCancelled, releases
// the claim, and stops further steps.
func TestApplyCancelAutomationRunCancelsActiveRun(t *testing.T) {
	conv := newGatedConversation(true)
	svc, db, id := newAsyncService(t, conv, Config{}, func(s *Spec) {
		s.Steps = []Step{
			{Kind: StepPrompt, Prompt: "step 0"},
			{Kind: StepPrompt, Prompt: "step 1"},
		}
	})
	w := watchOrFail(t, svc, id)
	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	runs := svc.Runs(id, 1)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	runID := runs[0].ID

	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.CancelAutomationRun{RunID: runID})
	if err != nil {
		t.Fatalf("Apply(CancelAutomationRun): %v", err)
	}
	last := drainSaveWithin(t, h, time.Second)
	if last.State != ports.SaveSaved {
		t.Fatalf("Apply(CancelAutomationRun) save event = %+v, want SaveSaved", last)
	}

	readUntilState(t, w, ports.RunCancelled, 2*time.Second)
	row, ok, err := db.GetAutomationRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.State != string(RunCancelled) || row.StepIndex != 0 {
		t.Fatalf("row = %+v, want cancelled at step 0", row)
	}
	svc.wg.Wait()
	if _, ok, err := svc.admitFire(context.Background(), id); err != nil || !ok {
		t.Fatalf("admitFire after CancelAutomationRun: ok=%v err=%v, want claim released", ok, err)
	}

	// Verify step 1 was never executed.
	stored, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.StepIndex != 0 {
		t.Fatalf("stored.StepIndex = %d, want 0 (step 1 was not run)", stored.StepIndex)
	}
}

// gatedSessionSpawner binds a real context-enabled session, so a run
// records a resumable session name, and hands every step to conv.
type gatedSessionSpawner struct {
	sess *chat.Session
	conv ports.Conversation
}

func (f *gatedSessionSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	if _, err := bind(f.sess); err != nil {
		return nil, err
	}
	return f.conv, nil
}

func (f *gatedSessionSpawner) SetApprovalOverride(string, func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, string) error {
	return nil
}

// TestRunOnceCancelledCtxMarksInterrupted proves a synchronous RunOnce
// whose caller ctx ends mid-step records RunInterrupted, and that
// ResumeRun then completes the run.
func TestRunOnceCancelledCtxMarksInterrupted(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	conv := newGatedConversation(true)
	id := seedEnabledAutomation(t, root, nil)
	svc, err := New(root, db, &gatedSessionSpawner{sess: newContextEnabledSession(t, db), conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		run ports.Run
		err error
	}
	out := make(chan result, 1)
	go func() {
		run, err := svc.RunOnce(ctx, id, ports.TriggerManual)
		out <- result{run, err}
	}()
	conv.waitStarted(t)
	cancel()
	var res result
	select {
	case res = <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("RunOnce did not return after ctx cancel")
	}
	if res.err != nil || res.run.State != ports.RunInterrupted {
		t.Fatalf("RunOnce after cancel = (%+v, %v), want RunInterrupted, nil", res.run, res.err)
	}

	close(conv.release)
	resumed, err := svc.ResumeRun(context.Background(), res.run.ID)
	if err != nil || resumed.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun = (%+v, %v), want RunSucceeded, nil", resumed, err)
	}
}

// TestApplyTriggerAfterCloseFailsRowAndReleasesClaim proves a fire
// admitted after Close is refused by name, its row is RunFailed with a
// message that says the run never started, and its claim is released.
// A trigger row has no session, so it is not resumable; RunInterrupted
// would promise a resume that admitResume refuses.
func TestApplyTriggerAfterCloseFailsRowAndReleasesClaim(t *testing.T) {
	svc, db, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	last := drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	if last.State != ports.SaveFailed || !strings.Contains(last.Message, ErrServiceClosed.Error()) {
		t.Fatalf("final save event = %+v, want SaveFailed naming %q", last, ErrServiceClosed.Error())
	}
	runs := svc.Runs(id, 1)
	if len(runs) != 1 || runs[0].State != ports.RunFailed || runs[0].Message != closedBeforeStartMessage {
		t.Fatalf("runs after closed trigger = %+v, want one RunFailed %q", runs, closedBeforeStartMessage)
	}
	if runs[0].FailKind != ports.RunFailJobError {
		t.Fatalf("FailKind = %v, want RunFailJobError", runs[0].FailKind)
	}
	if _, err := db.GetClaim(context.Background(), claimKey(id)); !errors.Is(err, storage.ErrClaimNotHeld) {
		t.Fatalf("GetClaim = %v, want ErrClaimNotHeld", err)
	}
}

// TestApplyResumeAfterCloseKeepsFailedRowAndReleasesClaim proves a
// resume admitted after Close leaves the RunFailed row as it was: same
// state, same message, claim released. Only the claim was new; the row
// stays resumable by a later Service.
func TestApplyResumeAfterCloseKeepsFailedRowAndReleasesClaim(t *testing.T) {
	svc, db, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx := context.Background()
	failed := Run{
		ID: "run-failed", AutomationID: id, Origin: "manual", State: RunFailed,
		StepIndex: 0, StepCount: 1, SessionName: automationSessionName(id, "run-failed"),
		StartedAt: time.Now().UTC(), FailKind: RunFailJobError, Message: "step 0 boom",
	}
	if err := svc.createRun(ctx, failed); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := svc.Close(cctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h, err := svc.Apply(ctx, ports.ScopeProject, ports.ResumeAutomationRun{RunID: failed.ID})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	last := drainSaveWithin(t, h, time.Second)
	if last.State != ports.SaveFailed || !strings.Contains(last.Message, ErrServiceClosed.Error()) {
		t.Fatalf("final save event = %+v, want SaveFailed naming %q", last, ErrServiceClosed.Error())
	}
	row, ok, gerr := db.GetAutomationRun(ctx, failed.ID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, gerr)
	}
	if row.State != string(RunFailed) || row.Message != "step 0 boom" {
		t.Fatalf("row after closed resume = state %q message %q, want the original RunFailed row", row.State, row.Message)
	}
	if _, err := db.GetClaim(ctx, claimKey(id)); !errors.Is(err, storage.ErrClaimNotHeld) {
		t.Fatalf("GetClaim = %v, want ErrClaimNotHeld", err)
	}
}

// TestInterruptStuckRunsLeavesSucceededRowUntouched proves the
// close-deadline interrupt never overwrites a run that finished in the
// read-write window, and publishes nothing for it.
func TestInterruptStuckRunsLeavesSucceededRowUntouched(t *testing.T) {
	svc, db, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx := context.Background()
	endedAt := time.Now().UTC()
	run := Run{ID: "run-done", AutomationID: id, State: RunSucceeded, StepIndex: 1, StepCount: 1, StartedAt: endedAt, EndedAt: &endedAt}
	if err := svc.createRun(ctx, run); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	w := watchOrFail(t, svc, id)
	svc.mu.Lock()
	svc.running[run.ID] = activeRun{cancel: func(error) {}, holder: "h", automationID: id}
	svc.mu.Unlock()

	svc.interruptStuckRuns(ctx)
	row, ok, err := db.GetAutomationRun(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.State != string(RunSucceeded) {
		t.Fatalf("row state = %q, want succeeded", row.State)
	}
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected publish %+v for an untouched row", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWatchAfterCloseReturnsClosedChannel proves Watch after Close
// registers nothing and hands back an already-closed stream.
func TestWatchAfterCloseReturnsClosedChannel(t *testing.T) {
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h, err := svc.Watch(context.Background(), id)
	if err != nil {
		t.Fatalf("Watch after Close: %v", err)
	}
	select {
	case _, ok := <-h.Events():
		if ok {
			t.Fatal("Watch after Close delivered an event, want a closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("Watch after Close: Events not closed")
	}
	svc.mu.Lock()
	n := len(svc.watchers[id])
	svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("Watch after Close registered %d watchers, want 0", n)
	}
	h.Cancel()
}

// TestFencedWriteAfterResumeRotatedTokenIsRejected proves a writer that
// still holds the old claim token cannot move the row after a resume
// rotated it, and that nothing is published for the rejected write.
func TestFencedWriteAfterResumeRotatedTokenIsRejected(t *testing.T) {
	svc, db, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	ctx := context.Background()
	old := Run{ID: "run-stale", AutomationID: id, State: RunInterrupted, StepCount: 1, SessionName: "s", ClaimToken: "old-token", StartedAt: time.Now().UTC()}
	if err := svc.createRun(ctx, old); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	adm, err := svc.admitResume(ctx, old.ID)
	if err != nil {
		t.Fatalf("admitResume: %v", err)
	}
	if adm.run.ClaimToken == old.ClaimToken {
		t.Fatal("admitResume did not rotate the claim token")
	}
	w := watchOrFail(t, svc, id)

	endedAt := time.Now().UTC()
	err = svc.updateRunStateFenced(ctx, old, RunFailed, 0, &endedAt, RunFailJobError, "stale")
	if !errors.Is(err, ErrRunFenced) {
		t.Fatalf("stale-token write err = %v, want ErrRunFenced", err)
	}
	svc.endRun(ctx, old, RunFailed, 0, RunFailJobError, "stale")
	select {
	case ev := <-w.Events():
		t.Fatalf("fenced-out write published %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	row, _, err := db.GetAutomationRun(ctx, old.ID)
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if row.State != string(RunInterrupted) || row.ClaimToken != adm.run.ClaimToken {
		t.Fatalf("row after fenced-out write = %+v", row)
	}
}

func TestCloseTimeoutMarksInterruptedAndReleasesClaim(t *testing.T) {
	conv := newGatedConversation(false)
	svc, db, id := newAsyncService(t, conv, Config{}, nil)
	// Registered after newTestDB, so it runs before the db closes: the
	// leaked goroutine stays blocked until here, then drains fully.
	t.Cleanup(func() {
		close(conv.release)
		svc.wg.Wait()
	})
	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := svc.Close(ctx); err == nil {
		t.Fatal("Close returned nil, want a timeout error")
	}
	runs := svc.Runs(id, 1)
	if len(runs) != 1 || runs[0].State != ports.RunInterrupted {
		t.Fatalf("runs after Close timeout = %+v, want one RunInterrupted", runs)
	}
	if _, err := db.GetClaim(context.Background(), claimKey(id)); !errors.Is(err, storage.ErrClaimNotHeld) {
		t.Fatalf("GetClaim after Close = %v, want ErrClaimNotHeld", err)
	}
}

func TestRunHandleCancelIsIdempotentUnderPublishBurst(t *testing.T) {
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	h, err := svc.Watch(context.Background(), id)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			svc.publishRun(Run{ID: "run-x", AutomationID: id, State: RunRunning})
		}
	}()
	go func() { defer wg.Done(); h.Cancel() }()
	go func() { defer wg.Done(); h.Cancel() }()
	wg.Wait()
	h.Cancel()
	select {
	case _, ok := <-h.Events():
		for ok {
			_, ok = <-h.Events()
		}
	case <-time.After(time.Second):
		t.Fatal("Events not closed after Cancel")
	}
}

func TestSendTurnHeadlessReturnsErrorOnCancelledTurn(t *testing.T) {
	conv := &scriptedConversation{events: []uievent.Event{
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "cancelled"}},
	}}
	_, err := sendTurnHeadless(context.Background(), conv, "x", time.Second)
	if err == nil {
		t.Fatal("cancelled turn returned nil error")
	}
}

func TestClaimRefreshedDuringLongStep(t *testing.T) {
	conv := newGatedConversation(true)
	svc, _, id := newAsyncService(t, conv, Config{ClaimRefreshInterval: 20 * time.Millisecond}, nil)
	w := watchOrFail(t, svc, id)
	drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	conv.waitStarted(t)

	time.Sleep(100 * time.Millisecond)
	n, err := svc.sweepInterrupted(context.Background(), 60*time.Millisecond)
	if err != nil || n != 0 {
		t.Fatalf("sweepInterrupted = (%d, %v), want (0, nil): claim was not refreshed", n, err)
	}
	close(conv.release)
	readUntilState(t, w, ports.RunSucceeded, 2*time.Second)
}

func TestApplyTriggerLostClaimIsSaveFailed(t *testing.T) {
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	if _, ok, err := svc.admitFire(context.Background(), id); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}
	last := drainSaveWithin(t, applyTrigger(t, svc, id), time.Second)
	if last.State != ports.SaveFailed || !strings.Contains(last.Message, ErrRunAlreadyActive.Error()) {
		t.Fatalf("final save event = %+v, want SaveFailed naming %q", last, ErrRunAlreadyActive.Error())
	}
}

func TestRunOnceStillReturnsSkippedNilOnLostClaim(t *testing.T) {
	svc, _, id := newAsyncService(t, newRecordingConversation(), Config{}, nil)
	if _, ok, err := svc.admitFire(context.Background(), id); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}
	run, err := svc.RunOnce(context.Background(), id, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce on lost claim err = %v, want nil", err)
	}
	if run.State != ports.RunSkipped {
		t.Fatalf("RunOnce on lost claim state = %v, want RunSkipped", run.State)
	}
}
