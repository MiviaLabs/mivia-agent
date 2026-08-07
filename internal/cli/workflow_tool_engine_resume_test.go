package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestSessionLaunchResumeReleasesHandoffClaimAfterRunFailure(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-session-handoff-release"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
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
	if _, err := engine.launchResume(ctx, prepared, false); err != nil {
		t.Fatalf("launchResume() error = %v", err)
	}
	<-started
	close(finishRun)
	<-closed
	if err := repo.ClaimRun(ctx, runID, "next-resumer"); err != nil {
		t.Fatalf("claim after failed session resume = %v", err)
	}
	if err := repo.ReleaseRun(ctx, runID, "next-resumer"); err != nil {
		t.Fatalf("release after failed session resume = %v", err)
	}
}
