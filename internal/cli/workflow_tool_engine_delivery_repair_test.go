package cli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newSessionAutoDeliveryRepairFixture builds the resume-engine state for the
// auto-delivery repair regression tests: a fixture run parked at
// delivery_pending whose admission snapshot carries delivery.on_failure
// routing to step "one", a seeded worktree change, and a resumePrepared wired
// to a Dispatcher-only build. The tests stub workflowResumeRun, so no real
// controller runs. repo is the assertion handle; the engine uses its own
// store handle like the production session engine.
func newSessionAutoDeliveryRepairFixture(t *testing.T) (*sessionWorkflowEngine, workflowledger.Repository, resumePrepared, string, *recordingPRClient) {
	t.Helper()
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	appendWorkflowDeliveryOnFailure(t, root, "one")
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	store, engineRepo, closeFn, err := openWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	finish, err := beginWorkflowExecution(root, contextStorePath(root, res.Subagents), runID)
	if err != nil {
		t.Fatal(err)
	}
	e := newSessionWorkflowEngine(root, configPath)
	p := resumePrepared{
		runID: runID, workflow: "two-step", root: root,
		built:      workflowControllerBuild{Dispatcher: workflowTestDispatcher{}},
		closeFn:    func() {},
		finishExec: finish,
		repo:       engineRepo,
		store:      store,
		res:        res,
	}
	return e, repo, p, runID, prRecorder
}

// settleRepairReadvance stands in for the real controller's terminal-route
// settle during a repair re-advance: a run the repair route re-entered at
// running settles back to delivery_pending (the real controller CASes
// delivery_pending when the repaired run reaches its success terminal under an
// active delivery policy). A run already at delivery_pending is returned
// unchanged. It runs on the engine goroutine, so it returns errors instead of
// failing the test.
func settleRepairReadvance(ctx context.Context, repo workflowledger.Repository, runID string) (workflowledger.RunSnapshot, error) {
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	if stored.Status != workflowledger.RunStatusRunning {
		return workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusDeliveryPending}, nil
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	return repo.GetRun(ctx, runID)
}

// repairGateDeliverGit fails every git command while fail is set, then
// delegates to RealGit. The re-advance test flips fail off during the second
// controller pass, so the second delivery attempt succeeds against the
// fixture origin.
type repairGateDeliverGit struct {
	mu   sync.Mutex
	fail bool
}

func (g *repairGateDeliverGit) Run(ctx context.Context, gctx delivery.GitContext, args ...string) (string, error) {
	g.mu.Lock()
	fail := g.fail
	g.mu.Unlock()
	if fail {
		return "", errors.New("test delivery repair failure")
	}
	return delivery.RealGit{}.Run(ctx, gctx, args...)
}

func (g *repairGateDeliverGit) setFail(v bool) {
	g.mu.Lock()
	g.fail = v
	g.mu.Unlock()
}

// TestSessionAutoDeliveryRepairReadvancesController is the regression test for
// the parked-run bug: a session-tool workflow whose auto-delivery fails with
// delivery.on_failure set must be re-advanced by the SAME goroutine. The
// repair route CASes the run back to running; no executor re-advanced it, so
// the run parked at running forever (DC-9). The first delivery fails with a
// plain error and routes the repair; the second controller pass returns
// delivery_pending again; the second delivery succeeds. The controller must be
// advanced twice, the run must settle succeeded, and exactly one PR must be
// created.
func TestSessionAutoDeliveryRepairReadvancesController(t *testing.T) {
	e, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)
	gate := &repairGateDeliverGit{fail: true}
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		if advanceCalls.Add(1) == 2 {
			gate.setFail(false)
		}
		return settleRepairReadvance(ctx, repo, runID)
	}
	workflowDeliverGit = gate

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after the repair re-advance delivered", run.Status)
	}
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("advance calls = %d, want 2 (the controller must re-advance once after the repair route)", got)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each on the successful second delivery", creates, finds)
	}
}

// TestSessionAutoDeliveryRepairBoundedAfterRepeatedFailures proves the repair
// loop is bounded: an always-failing delivery with on_failure set must not
// spin the controller. Each failure routes one wf-delivery attempt; the
// (MaxDeliveryRepairs+1)-th failure exhausts the repair budget and settles
// delivery_failed (terminal). The controller is advanced exactly one more
// time than the budget, then the goroutine exits. The budget default is
// configurable via delivery.max_repairs; the assertion tracks the constant so
// the test stays correct when the default changes.
func TestSessionAutoDeliveryRepairBoundedAfterRepeatedFailures(t *testing.T) {
	e, repo, p, runID, _ := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return settleRepairReadvance(ctx, repo, runID)
	}
	workflowDeliverGit = plainErrorDeliverGit{msg: "test delivery keeps failing"}

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want delivery_failed after the repair budget is spent", run.Status)
	}
	want := int32(delivery.MaxDeliveryRepairs + 1)
	if got := advanceCalls.Load(); got != want {
		t.Fatalf("advance calls = %d, want %d (one per repair attempt, then the budget settle)", got, want)
	}
}

// TestSessionAutoDeliveryRepairStepFailsTerminal proves the loop stops when
// the repair step itself fails: the second controller pass settles the run to
// failed (the terminal the step's failure route declares), so no third
// advance happens and the goroutine exits.
func TestSessionAutoDeliveryRepairStepFailsTerminal(t *testing.T) {
	e, repo, p, runID, _ := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		if advanceCalls.Add(1) == 2 {
			stored, err := repo.GetRun(ctx, runID)
			if err != nil {
				return workflowledger.RunSnapshot{}, err
			}
			if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusFailed, nil); err != nil {
				return workflowledger.RunSnapshot{}, err
			}
			return repo.GetRun(ctx, runID)
		}
		return workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusDeliveryPending}, nil
	}
	workflowDeliverGit = plainErrorDeliverGit{msg: "test delivery keeps failing"}

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed when the repair step fails", run.Status)
	}
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("advance calls = %d, want 2 (no re-advance after a terminal failed run)", got)
	}
}

// TestSessionAutoDeliveryRepairTransientStaysPending proves the loop does not
// dispatch a repair for a transport fault: with on_failure set, a transient
// delivery error (provider.TransientError) must leave the run delivery_pending
// (retryable), record ZERO wf-delivery repair attempts, and advance the
// controller exactly once.
func TestSessionAutoDeliveryRepairTransientStaysPending(t *testing.T) {
	e, repo, p, runID, _ := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return settleRepairReadvance(ctx, repo, runID)
	}
	workflowDeliverGit = transientDeliverGit{err: &provider.TransientError{Err: errors.New("connection reset")}}

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (retryable) after a transient delivery error", run.Status)
	}
	if got := advanceCalls.Load(); got != 1 {
		t.Fatalf("advance calls = %d, want 1 (a transport fault must not dispatch the repair step)", got)
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			t.Fatalf("wf-delivery repair attempt recorded after a transient failure: %+v", a)
		}
	}
}

// transientDeliverGit fails every git command with a fixed transient error.
type transientDeliverGit struct{ err error }

func (t transientDeliverGit) Run(_ context.Context, _ delivery.GitContext, _ ...string) (string, error) {
	return "", t.err
}

// TestSessionAutoDeliveryRepairCancelDuringLoop proves stopActive during the
// repair loop stops the loop: the second controller pass blocks until runCtx
// is cancelled, stopActive cancels it, the goroutine exits, the run stays
// running (repair-routed, resumable by a later session tool call), and no
// further advance happens.
func TestSessionAutoDeliveryRepairCancelDuringLoop(t *testing.T) {
	e, repo, p, runID, _ := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		if advanceCalls.Add(1) >= 2 {
			<-ctx.Done()
			return workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusRunning}, nil
		}
		return workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusDeliveryPending}, nil
	}
	workflowDeliverGit = plainErrorDeliverGit{msg: "test delivery keeps failing"}

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	e.stopActive(context.Background(), runID)
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running (the repair route stays; cancel must not settle it)", run.Status)
	}
	// The goroutine is idle: done is closed and the active entry is removed,
	// so no further advance can run. The counter check is deterministic.
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("advance calls = %d, want 2 (no advance after the loop is cancelled)", got)
	}
}

// TestSessionAutoDeliveryRepairHappyPathFirstDeliverySucceeds proves the loop
// exits after a single successful delivery: the first advance settles
// delivery_pending, the delivery succeeds against the fixture origin, the run
// settles succeeded, and the controller is advanced exactly once (no repair
// re-advance on the happy path).
func TestSessionAutoDeliveryRepairHappyPathFirstDeliverySucceeds(t *testing.T) {
	e, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return settleRepairReadvance(ctx, repo, runID)
	}
	// RealGit delivery: the fixture origin accepts the push and
	// workflowDeliverNewPR records the PR (see the re-advance test's second
	// delivery, which uses the same RealGit path).
	workflowDeliverGit = delivery.RealGit{}

	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after the first delivery succeeds", run.Status)
	}
	if got := advanceCalls.Load(); got != 1 {
		t.Fatalf("advance calls = %d, want 1 (no repair re-advance on the happy path)", got)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each on the successful first delivery", creates, finds)
	}
}

// TestSessionAutoDeliveryRepairAllowPublishFalse proves the loop does not
// deliver or re-advance when allow_publish is false: the run stays
// delivery_pending (retryable via the operator deliver path), the controller
// is advanced exactly once, and no PR client call happens. A failing git stub
// proves delivery is never attempted.
func TestSessionAutoDeliveryRepairAllowPublishFalse(t *testing.T) {
	e, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)
	var advanceCalls atomic.Int32
	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return settleRepairReadvance(ctx, repo, runID)
	}
	workflowDeliverGit = plainErrorDeliverGit{msg: "must not be used without allow-publish"}

	if _, err := e.launchResume(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (not published, not repaired)", run.Status)
	}
	if got := advanceCalls.Load(); got != 1 {
		t.Fatalf("advance calls = %d, want 1 (no re-advance without allow_publish)", got)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero when allow_publish is false", creates, finds)
	}
}
