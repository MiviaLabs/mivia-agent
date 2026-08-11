package cli

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// parkedSweepHolder is the claim holder the parked-run sweep's resume build
// claims with in wireParkedResumeStubs; tests that CAS the run under the
// claim bind the same holder context.
const parkedSweepHolder = "parked-sweep-holder"

// TestSessionReconcileParkedRunsDelivers proves a delivery_pending run left
// over from an earlier session (a restart or a crash) is published when the
// harness wires its workflow surface. Delivery authorization comes from the
// workflow's [delivery] policy - no allow_publish flag and no manual override
// is involved (the session launch path carries none). The run must settle
// succeeded and exactly one PR must be created.
func TestSessionReconcileParkedRunsDelivers(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	var opts tools.DefaultOptions
	// A non-nil event-bus provider marks production wiring and arms the
	// parked-run sweep; the nil-provider test paths never sweep.
	wireWorkflowToolOptions(&opts, root, res, func() *events.Bus { return nil })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusDeliveryPending {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (parked run published by the session-start sweep)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one create and one find", creates, finds)
	}
}

// wireParkedResumeStubs replaces the resume machinery like
// wireExecuteResumeDeliveryStubs, but the build carries a controller-bearing
// build: the claim handoff (claimWorkflowResumeHandoff) only gates when the
// controller is non-nil, so the parked-run sweep exercises the real claim
// fence. The advance stub replaces workflowResumeRun, so no real controller
// runs.
func wireParkedResumeStubs(t *testing.T, repo workflowledger.Repository, advance func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error)) {
	t.Helper()
	originalOpen := workflowResumeOpenStore
	originalHooks := workflowResumeInstallHooks
	originalBuild := workflowResumeBuild
	originalAdmission := workflowResumeSetAdmission
	originalForce := workflowResumeSetForce
	originalRun := workflowResumeRun
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeInstallHooks = originalHooks
		workflowResumeBuild = originalBuild
		workflowResumeSetAdmission = originalAdmission
		workflowResumeSetForce = originalForce
		workflowResumeRun = originalRun
	})
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeInstallHooks = func(string, bool) (func(), error) { return func() {}, nil }
	workflowResumeBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, _ workflowledger.Repository, _ *compiler.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, _ string, _ *workflowledger.Snapshot, recorded *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		ctrl := &controller.LinearController{Holder: parkedSweepHolder}
		if recorded != nil {
			ctrl.RunID = recorded.RunID
		}
		return workflowControllerBuild{Dispatcher: workflowTestDispatcher{}, Controller: ctrl}, nil
	}
	workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
	workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
	workflowResumeRun = advance
}

// TestSessionReconcileParkedRunsResumesRunningRun proves a run left at
// running by an earlier session (its claim was released on graceful shutdown)
// is auto-resumed by the parked-run sweep: the sweep claims it fresh and
// re-advances the controller. The advance stub settles the run to succeeded;
// the run must end succeeded with exactly one controller advance.
func TestSessionReconcileParkedRunsResumesRunningRun(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		// Writes are fenced to the claim holder the sweep claimed with; bind
		// the holder context like the real controller does.
		claimCtx := workflowledger.ContextWithClaimHolder(ctx, parkedSweepHolder)
		if err := repo.CompareAndSetRunStatus(claimCtx, runID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		return repo.GetRun(ctx, runID)
	})

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	e := newSessionWorkflowEngine(root, configPath)

	e.reconcileParkedRuns(context.Background())
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (sweep resumed the interrupted run)", run.Status)
	}
	if got := advanceCalls.Load(); got != 1 {
		t.Fatalf("controller advances = %d, want 1", got)
	}
}

// TestSessionReconcileParkedRunsSkipsLiveClaim proves the sweep never
// double-executes a run another session is still running: a fresh claim
// within its lease belongs to a live holder, the claim handoff refuses the
// takeover, prepareResume fails synchronously, and the sweep moves on. The
// advance stub must never be called and the run must stay running.
func TestSessionReconcileParkedRunsSkipsLiveClaim(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return repo.GetRun(ctx, runID)
	})
	ctx := context.Background()
	if err := repo.ClaimRun(ctx, runID, "live-session"); err != nil {
		t.Fatal(err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	e := newSessionWorkflowEngine(root, configPath)

	// The sweep processes runs synchronously; the live-claim run is refused at
	// the claim handoff, so when this returns the sweep is done with it.
	e.reconcileParkedRuns(ctx)

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running (sweep must skip a live-claimed run)", run.Status)
	}
	if got := advanceCalls.Load(); got != 0 {
		t.Fatalf("controller advances = %d, want 0 (live-claimed run must not be resumed)", got)
	}
}

// seedTwoParkedRunningRuns builds one store with two interrupted (running,
// unclaimed) runs of the resume fixture, so the parallel sweep has two
// resumable runs to dispatch at once. No session ever claimed them: they model
// a graceful shutdown that released both claims.
func seedTwoParkedRunningRuns(t *testing.T) (root, configPath string, repo workflowledger.Repository, runIDs []string) {
	t.Helper()
	root = t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)

	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = workflowledger.NewStorageRepository(store)
	runIDs = []string{"wfr-parallel-a", "wfr-parallel-b"}
	for _, runID := range runIDs {
		identity, err := workflowspace.Ensure(ctx, root, runID, workflowspace.IsolationWorktree)
		if err != nil {
			t.Fatal(err)
		}
		remoteURL, err := workflowDeliveryAdmission(compiled, identity, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(identity.Root, "change.txt"), []byte("seeded change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := workflowledger.RunSnapshot{
			RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
			SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot), InputDigest: workflowledger.InputDigest(snapshot.Inputs),
			Status: workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
			BaseRef: identity.BaseRef, BaseCommit: identity.BaseCommit, OriginBaseCommit: identity.OriginBaseCommit,
			WorktreeName: identity.WorktreeName, RemoteURL: remoteURL,
		}
		if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, "config.toml"), repo, runIDs
}

// TestSessionReconcileParkedRunsResumesInParallel proves the sweep fans out
// per run instead of serializing: two interrupted runs in one store are both
// resumed by a single sweep pass, neither waiting for the other's dispatch.
// Both must settle succeeded with exactly two controller advances.
func TestSessionReconcileParkedRunsResumesInParallel(t *testing.T) {
	root, configPath, repo, runIDs := seedTwoParkedRunningRuns(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		runID := b.Controller.RunID
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		// Writes are fenced to the claim holder the sweep claimed with; bind
		// the holder context like the real controller does.
		claimCtx := workflowledger.ContextWithClaimHolder(ctx, parkedSweepHolder)
		if err := repo.CompareAndSetRunStatus(claimCtx, runID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		return repo.GetRun(ctx, runID)
	})

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	e := newSessionWorkflowEngine(root, configPath)

	e.reconcileParkedRuns(context.Background())
	for _, runID := range runIDs {
		waitForSessionEngineIdle(t, e, runID)
		run, err := repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusSucceeded {
			t.Fatalf("run %s status = %q, want succeeded (parallel sweep resumed both runs)", runID, run.Status)
		}
	}
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("controller advances = %d, want 2 (one per resumed run)", got)
	}
}
