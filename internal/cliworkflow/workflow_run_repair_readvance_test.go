package cliworkflow

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestExecuteWorkflowResumeReadvancesAfterRepeatedDeliveryRepair is the
// regression test for the parked-run bug: a repairable delivery failure
// routes the run back to RunStatusRunning at its repair step
// (delivery.ReopenForRepair), and nothing used to re-advance it - the
// foreground CLI process (workflow run or workflow resume) just printed the
// new status and exited, leaving the run parked with no live process until a
// human ran `mivia workflow resume <id>` by hand. Live finding: two real runs
// sat parked 20+ minutes each this way.
//
// This reproduces the SECOND hang in that chain: the manual recovery resume
// itself re-enters the repair step, delivery fails AGAIN, and the run must
// still not be left parked - `workflow resume` must drive it all the way to
// a terminal status in one invocation, exactly like `workflow run` and
// `workflow resume` already claim to own the run until completion ("CLI
// foreground paths are unbounded by design", workflow_run.go).
func TestExecuteWorkflowResumeReadvancesAfterRepeatedDeliveryRepair(t *testing.T) {
	root, storePath, cfg, prRecorder := newDeliveryFixture(t)
	appendWorkflowDeliveryOnFailure(t, root, "one")
	runID := runFixtureToDeliveryPending(t, root, cfg)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	gate := &repairGateDeliverGit{fail: true}
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() { WorkflowDeliverGit = originalGit })
	WorkflowDeliverGit = gate

	// First delivery attempt (a plain `workflow deliver`) fails and routes
	// the run back to running at its repair step - reproducing the live
	// parked state.
	if err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", cfg, "--allow-publish"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("deliver (first, expected repair route) error = %v", err)
	}
	parked, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status after first delivery failure = %q, want %q", parked.Status, workflowledger.RunStatusRunning)
	}

	advanceCalls := stubWorkflowResumeRepairReadvance(t, repo, runID, gate)

	// The manual recovery step a human would run today.
	err = RunWorkflowWithIO([]string{"resume", runID, "--workspace", root, "--config", cfg, "--allow-publish"}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("resume error = %v, want nil: the CLI must drive the run to completion instead of parking again", err)
	}

	final, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want %q: a repeated repairable delivery failure must not leave the run parked at running with no process driving it",
			final.Status, workflowledger.RunStatusSucceeded)
	}
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("advance calls = %d, want 2 (the resume's own advance, then the auto re-entry after the second delivery failure)", got)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each on the eventual successful delivery", creates, finds)
	}
}

// stubWorkflowResumeRepairReadvance stubs the resume-build seams so
// WorkflowResumeRun stands in for the real controller. Controller is left
// nil in the build so prepareWorkflowResumeExecution skips the handoff claim
// entirely instead of claiming it through this test's fake build (mirrors
// newSessionAutoDeliveryRepairFixture's fixture). The returned counter's
// FIRST call (the resume the test drives) leaves gate failing, so its own
// delivery attempt fails again and re-enters repair - the combination the
// bug never handled; the SECOND call (only reachable through the fix's auto
// re-entry) clears gate, so that delivery attempt succeeds.
func stubWorkflowResumeRepairReadvance(t *testing.T, repo workflowledger.Repository, runID string, gate *repairGateDeliverGit) *atomic.Int32 {
	t.Helper()
	var advanceCalls atomic.Int32
	originalBuild := workflowResumeBuild
	originalAdmission := WorkflowResumeSetAdmission
	originalForce := WorkflowResumeSetForce
	originalHooks := WorkflowResumeInstallHooks
	originalRun := WorkflowResumeRun
	t.Cleanup(func() {
		workflowResumeBuild = originalBuild
		WorkflowResumeSetAdmission = originalAdmission
		WorkflowResumeSetForce = originalForce
		WorkflowResumeInstallHooks = originalHooks
		WorkflowResumeRun = originalRun
	})
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *definition.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, []byte, *workflowledger.RunSnapshot, map[string]bool, *skills.Registry) (WorkflowControllerBuild, error) {
		return WorkflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, nil
	}
	WorkflowResumeSetAdmission = func(WorkflowControllerBuild) error { return nil }
	WorkflowResumeSetForce = func(WorkflowControllerBuild) error { return nil }
	WorkflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	WorkflowResumeRun = func(ctx context.Context, _ WorkflowControllerBuild) (workflowledger.RunSnapshot, error) {
		if advanceCalls.Add(1) == 2 {
			gate.setFail(false)
		}
		return settleRepairReadvance(ctx, repo, runID)
	}
	return &advanceCalls
}
