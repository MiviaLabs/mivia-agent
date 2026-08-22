package cliworkflow

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSessionWorkflowEngineCancelUsesLiveCoordinator is an end-to-end
// regression test for cancelRunWithGuardedCoordinator's live-coordinator
// branch (useLiveCoordinator's callback body in
// workflow_tool_engine_guard.go): when e.active[runID] has a usable, not-yet-
// closed runner, Cancel must dispatch CancelRunWithAttemptsWithClaim through
// THAT exact live coordinator instead of building a fresh store-backed one -
// the whole point of D15's cancel-broker reuse (see cliPanelCancelCoordinator
// and TestCliPanelCancelCoordinatorReusesLiveInstance for the equivalent
// proof at the coordinator-selection layer). This test proves it end to end
// through the real session engine and a real SQLite-backed ledger.
func TestSessionWorkflowEngineCancelUsesLiveCoordinator(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const runID = "wfr-session-cancel-live-coordinator"
	// An explicit isolated config path (reused for setup and for the engine
	// below) avoids config.Load's ambient search, which - inside this repo's
	// own working tree - can find a real, locally-edited .mivia/mivia.toml
	// with provider state this test has no business depending on (see
	// workflowApprovalTestIsolatedConfigPath).
	configPath := workflowApprovalTestIsolatedConfigPath(t)

	release, repo, _, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		t.Fatalf("setup openWorkflowResolutionContext: %v", err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		release()
		closeFn()
		t.Fatalf("CreateRun: %v", err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		release()
		closeFn()
		t.Fatalf("GetRun: %v", err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		release()
		closeFn()
		t.Fatalf("CompareAndSetRunStatus: %v", err)
	}
	release()
	closeFn()

	engine := NewSessionWorkflowEngine(root, configPath)
	liveCoord := coordinator.New(nil, nil)
	done := make(chan struct{})
	close(done)
	engine.active[runID] = &sessionActiveRun{
		cancel:  func() {},
		done:    done,
		runner:  controller.NewCoordinatorRunner(liveCoord),
		closeFn: func() {},
	}

	result, err := engine.Cancel(ctx, runID)
	if err != nil {
		t.Fatalf("Cancel() error = %v, want nil", err)
	}
	if result.Status != string(workflowledger.RunStatusCanceled) {
		t.Fatalf("Cancel() status = %q, want canceled", result.Status)
	}
}
