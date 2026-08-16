package cli

// Parallel and periodic sweep resume tests: two-parked-runs fan-out and
// mid-session claim-expiry recovery. Split from
// workflow_tool_engine_reconcile_test.go for the 800-line file limit.

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

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

	e.reconcileParkedRuns(context.Background(), false)
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

// TestSessionReconcileParkedRunsPeriodic proves a run whose claim expires
// mid-session is picked up by the periodic re-scan without any session start:
// the scan runs while the session is up, and the first tick recovers the
// parked run on its own. The scan is quiet, so the per-tick expected skips
// (active runs) never log.
func TestSessionReconcileParkedRunsPeriodic(t *testing.T) {
	root, configPath, repo, runIDs := seedTwoParkedRunningRuns(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		runID := b.Controller.RunID
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			return workflowledger.RunSnapshot{}, err
		}
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

	old := workflowReconcileInterval.Load()
	workflowReconcileInterval.Store(int64(10 * time.Millisecond))
	t.Cleanup(func() { workflowReconcileInterval.Store(old) })
	ctx, cancel := context.WithCancel(context.Background())
	periodicDone := make(chan struct{})
	go func() {
		defer close(periodicDone)
		e.reconcileParkedRunsPeriodic(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		done := 0
		for _, runID := range runIDs {
			run, err := repo.GetRun(context.Background(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status == workflowledger.RunStatusSucceeded {
				done++
			}
		}
		if done == len(runIDs) {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-periodicDone
	for _, runID := range runIDs {
		waitForSessionEngineIdle(t, e, runID)
	}

	for _, runID := range runIDs {
		run, err := repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusSucceeded {
			t.Fatalf("run %s status = %q, want succeeded (periodic scan recovered it without a session start)", runID, run.Status)
		}
	}
	if got := advanceCalls.Load(); got != 2 {
		t.Fatalf("controller advances = %d, want 2 (one per recovered run)", got)
	}
}
