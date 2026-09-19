package localengine

// Engine-side runner for the shared stack-driver conformance catalog
// (internal/workflows/delivery/stack_conformance.go). Every scenario the
// catalog labels OwnerDriverEngine must be handled by the switch below; an
// unhandled engine-owned row fails this test, so a catalog addition cannot
// silently skip the engine driver.

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowagenttools "github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// confPrunedPR answers IsMerged without ever resolving FindByHead: the remote
// ref is gone (squash merge with delete-branch), but the PR state still says
// merged. Any driver that requires FindByHead to resolve first stalls forever
// on this oracle - the sibling drift the shared probe removed.
type confPrunedPR struct{}

func (confPrunedPR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (confPrunedPR) Create(_ context.Context, _ string, in delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{RemoteID: "pr-1"}, nil
}

func (confPrunedPR) IsMerged(context.Context, string, string) (bool, error) {
	return true, nil
}

func confSeedStack(t *testing.T, chunks []delivery.ChunkPlan) (*workflowledger.Store, workflowledger.Repository) {
	t.Helper()
	ledgerStore := workflowledger.NewStore(storage.NewMemory())
	if err := delivery.SeedStackLedger(context.Background(), ledgerStore, "stack-conf", chunks); err != nil {
		t.Fatalf("SeedStackLedger: %v", err)
	}
	return ledgerStore, workflowledger.NewMemoryRepository()
}

func confSeedChunkRun(t *testing.T, repo workflowledger.Repository, chunkID string, status workflowledger.RunStatus, remoteURL string) workflowledger.RunSnapshot {
	t.Helper()
	key := "stack-conf:" + chunkID
	runID := workflowagenttools.InvocationRunID(key)
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", InvocationKey: key,
		Status: workflowledger.RunStatusPending, RemoteURL: remoteURL, WorktreeName: "workflow-" + chunkID, BaseRef: "main",
	}
	if err := repo.CreateRun(context.Background(), snap, []byte("{}")); err != nil {
		t.Fatalf("CreateRun(%s): %v", chunkID, err)
	}
	created, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, created.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatalf("CAS %s running: %v", chunkID, err)
	}
	running, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, running.Version, status, &now); err != nil {
		t.Fatalf("CAS %s to %s: %v", chunkID, status, err)
	}
	snap.Status = status
	return snap
}

func confSeedPushedRecord(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, headBranch string) {
	t.Helper()
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: delivery.DeliveryKey(run.RunID, "d"),
		Mode: "draft", BaseRef: "main", HeadRef: headBranch,
		Status: "pushed", CommitSHA: "deadbeef" + run.RunID[len(run.RunID)-4:],
	}); err != nil {
		t.Fatalf("UpsertDelivery(%s): %v", run.RunID, err)
	}
}

func confExhaustAttempts(t *testing.T, e *Engine, ledgerStore *workflowledger.Store, chunkID string) {
	t.Helper()
	// The reopen decision reads the task from running, the only status a
	// chunk's failure path is reached from.
	if err := ledgerStore.TransitionTask("stack-conf", chunkID, delivery.StatusRunning); err != nil {
		t.Fatalf("seed %s running: %v", chunkID, err)
	}
	for i := 0; i < delivery.MaxChunkAttempts; i++ {
		if !e.reopenOrFailStackTask(context.Background(), ledgerStore, "stack-conf", chunkID) {
			t.Fatalf("reopen %d for %s reported no progress", i+1, chunkID)
		}
		if err := ledgerStore.TransitionTask("stack-conf", chunkID, delivery.StatusRunning); err != nil {
			t.Fatalf("reset %s running after reopen %d: %v", chunkID, i+1, err)
		}
	}
	// Budget spent: the driver must now mark the chunk failed (the mark IS
	// progress).
	if !e.reopenOrFailStackTask(context.Background(), ledgerStore, "stack-conf", chunkID) {
		t.Fatalf("final reopen-or-fail for %s must mark the chunk failed", chunkID)
	}
}

// confDrivePasses runs the engine's settle and merge-observation passes
// exactly as driveStackLoop does.
func confDrivePasses(t *testing.T, e *Engine, ledgerStore *workflowledger.Store) {
	t.Helper()
	byID, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-conf")
	if err != nil {
		t.Fatal(err)
	}
	e.processSettledChunks(context.Background(), ledgerStore, "stack-conf", byID, true)
	byID, err = delivery.TaskMap(context.Background(), ledgerStore, "stack-conf")
	if err != nil {
		t.Fatal(err)
	}
	e.markMergedChunks(context.Background(), ledgerStore, "stack-conf", byID)
}

// confSeedChunk applies one chunk's scenario events as durable state.
func confSeedChunk(t *testing.T, e *Engine, ledgerStore *workflowledger.Store, repo workflowledger.Repository, scenario delivery.StackConformanceScenario, c delivery.ConformanceScenarioChunk) {
	t.Helper()
	failed, canceled := false, false
	for _, ev := range c.Events {
		switch ev {
		case "merged":
			// The durable in-flight fact: the chunk delivered and its run
			// settled succeeded (markMergedChunks probes published/
			// implemented tasks only).
			snap := confSeedChunkRun(t, repo, c.ID, workflowledger.RunStatusSucceeded, "https://github.com/acme/widgets")
			confSeedPushedRecord(t, repo, snap, "wf/"+snap.WorktreeName)
			if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusPublished); err != nil {
				t.Fatalf("seed %s published: %v", c.ID, err)
			}
		case "pruned-branch":
			// Modeled by confPrunedPR: FindByHead never resolves; only
			// IsMerged answers.
		case "failed":
			failed = true
		case "canceled":
			canceled = true
		default:
			t.Fatalf("scenario %q: engine runner does not model event %q", scenario.Name, ev)
		}
	}
	switch {
	case failed:
		confSeedChunkRun(t, repo, c.ID, workflowledger.RunStatusFailed, "https://github.com/acme/widgets")
		confExhaustAttempts(t, e, ledgerStore, c.ID)
	case canceled:
		// The durable fact is the canceled task; a drive pass must leave it
		// canceled (no reopen, no merge).
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusRunning); err != nil {
			t.Fatalf("seed %s running: %v", c.ID, err)
		}
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusCanceled); err != nil {
			t.Fatalf("seed %s canceled: %v", c.ID, err)
		}
	}
}

// TestStackConformanceEngine runs every engine-owned catalog scenario through
// the engine's drive passes and asserts the shared expected end states.
func TestStackConformanceEngine(t *testing.T) {
	for _, scenario := range delivery.StackConformanceScenarios() {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			if !scenario.ScenarioOwnedBy(delivery.OwnerDriverEngine) {
				if scenario.ScenarioOwnedBy(delivery.OwnerCLIOnly) {
					t.Logf("scenario %q is CLI-only (engine has no merge authority): skipped", scenario.Name)
					return
				}
				t.Fatalf("scenario %q is owned by %v but not wired into this runner", scenario.Name, scenario.Owners)
			}
			var plans []delivery.ChunkPlan
			for _, c := range scenario.Chunks {
				plans = append(plans, delivery.ChunkPlan{ID: c.ID, Title: c.ID, Files: c.Files, DependsOn: c.Deps})
			}
			ledgerStore, repo := confSeedStack(t, plans)
			e := &Engine{Repo: repo, PR: confPrunedPR{}}

			for _, c := range scenario.Chunks {
				confSeedChunk(t, e, ledgerStore, repo, scenario, c)
			}
			confDrivePasses(t, e, ledgerStore)

			after, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-conf")
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range scenario.Chunks {
				if got, want := after[c.ID].Status, scenario.Expect[c.ID]; got != want {
					t.Errorf("chunk %s end status = %q, want %q", c.ID, got, want)
				}
			}
		})
	}
}
