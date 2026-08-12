package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSessionLaunchResumeCapturesLiveCoordinatorRunner is a regression test
// (Wave 7 hostile audit finding): launchResume used to build sessionActiveRun
// without its runner field, so a session-engine cancel of a RESUMED run could
// never reuse the live coordinator that actually dispatched its panel
// children (see launchStartedWorkflow, the new-run path, which was fixed
// first). A cancel would then always fall back to a fresh, cross-process
// coordinator - which cancelRecovered fail-closed refuses ("no live
// execution owner") for a task the live coordinator is still dispatching.
func TestSessionLaunchResumeCapturesLiveCoordinatorRunner(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-session-resume-runner-capture"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	liveCoord := coordinator.New(nil, nil)
	liveRunner := controller.NewCoordinatorRunner(liveCoord)
	linear := &controller.LinearController{Holder: "session-resumer", Runner: liveRunner}
	if err := repo.ClaimRun(ctx, runID, linear.Holder); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finishRun := make(chan struct{})
	closed := make(chan struct{})
	originalRun := workflowResumeRun
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		close(started)
		<-finishRun
		return workflowledger.RunSnapshot{}, errors.New("injected run failure")
	}
	t.Cleanup(func() { workflowResumeRun = originalRun })

	engine := newSessionWorkflowEngine(".", "")
	prepared := resumePrepared{
		runID:    runID,
		workflow: "test",
		built: workflowControllerBuild{
			Controller: linear,
			Dispatcher: workflowTestDispatcher{},
		},
		repo:       repo,
		finishExec: func() {},
		closeFn:    func() { close(closed) },
	}
	if _, err := engine.launchResume(ctx, prepared); err != nil {
		t.Fatalf("launchResume() error = %v", err)
	}
	engine.mu.Lock()
	active := engine.active[runID]
	engine.mu.Unlock()
	if active == nil || active.runner != liveRunner {
		t.Fatalf("sessionActiveRun.runner = %p, want the live coordinator runner %p that launchResume actually dispatches through", activeRunner(active), liveRunner)
	}
	<-started
	close(finishRun)
	<-closed
}

func activeRunner(a *sessionActiveRun) *controller.CoordinatorRunner {
	if a == nil {
		return nil
	}
	return a.runner
}

func TestSessionLaunchResumeReleasesHandoffClaimAfterRunFailure(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-session-handoff-release"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	// The run is already admitted and running when the session resume takes
	// over, exactly as a resume of an interrupted run looks.
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	controller := &controller.LinearController{Holder: "session-resumer"}
	if err := repo.ClaimRun(ctx, runID, controller.Holder); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finishRun := make(chan struct{})
	closed := make(chan struct{})
	originalRun := workflowResumeRun
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		close(started)
		<-finishRun
		return workflowledger.RunSnapshot{}, errors.New("injected run failure")
	}
	t.Cleanup(func() { workflowResumeRun = originalRun })

	engine := newSessionWorkflowEngine(".", "")
	prepared := resumePrepared{
		runID:    runID,
		workflow: "test",
		built: workflowControllerBuild{
			Controller: controller,
			Dispatcher: workflowTestDispatcher{},
		},
		repo:       repo,
		finishExec: func() {},
		closeFn:    func() { close(closed) },
	}
	if _, err := engine.launchResume(ctx, prepared); err != nil {
		t.Fatalf("launchResume() error = %v", err)
	}
	<-started
	close(finishRun)
	<-closed
	// The settle must run AFTER the handoff release: it claims the run with its
	// own holder, so a still-held handoff claim makes it a no-op and the run
	// would stay running with no cause.
	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status after failed session resume = %q, want failed: a run whose controller stopped must not stay running", after.Status)
	}
	if err := repo.ClaimRun(ctx, runID, "next-resumer"); err != nil {
		t.Fatalf("claim after failed session resume = %v", err)
	}
	if err := repo.ReleaseRun(ctx, runID, "next-resumer"); err != nil {
		t.Fatalf("release after failed session resume = %v", err)
	}
}
