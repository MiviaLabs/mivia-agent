package workflow

// CLI-side runner for the shared stack-driver conformance catalog
// (internal/workflows/delivery/stack_conformance.go). Every scenario the
// catalog labels OwnerDriverCLI (or OwnerCLIOnly) must be handled by the
// switch below; an unhandled row fails this test, so a catalog addition
// cannot silently skip the CLI driver. The full-loop behavioural coverage
// for each row lives in the drive integration suites (stack_drive_*_test.go,
// session_stack_drive_integration_test.go); this runner pins the SHARED
// expected ledger end states on the same fixtures' primitives so both
// drivers prove the same contract.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowagenttools "github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// confFakeGit scripts the git answers the merge-observation probes need:
// merge-base --is-ancestor exits 1 (confident "not an ancestor", the squash
// case), and name-only diffs are configurable per ref range.
type confFakeGit struct {
	overlapFiles []string
}

func (g confFakeGit) Run(_ context.Context, _ delivery.GitContext, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "merge-base --is-ancestor"):
		return "", &confExitError{code: 1}
	case strings.Contains(joined, "merge-base"):
		return "mb0000", nil
	case strings.Contains(joined, "diff --name-only"):
		return strings.Join(g.overlapFiles, "\n"), nil
	case strings.Contains(joined, "fetch"):
		return "", nil
	}
	return "", nil
}

// confExitError mimics exec.ExitError with an exit code, the shape the probe
// reads to tell git's confident "no" from a probe failure.
type confExitError struct{ code int }

func (e *confExitError) Error() string { return "exit status" }
func (e *confExitError) ExitCode() int { return e.code }

// confFakePR answers merged for every head, without requiring FindByHead to
// resolve: the remote PR state survives a pruned branch.
type confFakePR struct{}

func (confFakePR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (confFakePR) Create(_ context.Context, _ string, in delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{RemoteID: "pr-1"}, nil
}

func (confFakePR) IsMerged(context.Context, string, string) (bool, error) {
	return true, nil
}

func confSeedCLIStack(t *testing.T, chunks []delivery.ChunkPlan) (*workflowledger.Store, workflowledger.Repository) {
	t.Helper()
	ledgerStore := workflowledger.NewStore(storage.NewMemory())
	if err := delivery.SeedStackLedger(context.Background(), ledgerStore, "stack-conf", chunks); err != nil {
		t.Fatalf("SeedStackLedger: %v", err)
	}
	return ledgerStore, workflowledger.NewMemoryRepository()
}

func confSeedCLIRun(t *testing.T, repo workflowledger.Repository, chunkID string) string {
	t.Helper()
	key := "stack-conf:" + chunkID
	runID := workflowagenttools.InvocationRunID(key)
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", InvocationKey: key,
		Status: workflowledger.RunStatusPending, RemoteURL: "https://github.com/acme/widgets",
		WorktreeName: chunkID, BaseRef: "main",
	}
	if err := repo.CreateRun(context.Background(), snap, []byte("{}")); err != nil {
		t.Fatalf("CreateRun(%s): %v", chunkID, err)
	}
	return runID
}

func confSeedCLIPushedRecord(t *testing.T, repo workflowledger.Repository, runID, headBranch string) {
	t.Helper()
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: delivery.DeliveryKey(runID, "d"),
		Mode: "draft", BaseRef: "main", HeadRef: headBranch,
		Status: "pushed", CommitSHA: "cafe1234",
	}); err != nil {
		t.Fatalf("UpsertDelivery(%s): %v", runID, err)
	}
}

// confExhaustCLIBudget exhausts the attempt budget through the CLI driver's
// own reconcile decision, exactly as the recovery sweep does: bounded
// reopens, then mark_failed.
func confExhaustCLIBudget(t *testing.T, ledgerStore *workflowledger.Store, chunkID string) {
	t.Helper()
	if err := ledgerStore.TransitionTask("stack-conf", chunkID, delivery.StatusRunning); err != nil {
		t.Fatalf("seed %s running: %v", chunkID, err)
	}
	for i := 0; i < delivery.MaxChunkAttempts; i++ {
		act, err := reconcileReopenOrFail(ledgerStore, "stack-conf", chunkID)
		if err != nil {
			t.Fatalf("reopen %d for %s: %v", i+1, chunkID, err)
		}
		if err := applyReconcileAction(ledgerStore, "stack-conf", act); err != nil {
			t.Fatalf("apply reopen %d for %s: %v", i+1, chunkID, err)
		}
		if err := ledgerStore.TransitionTask("stack-conf", chunkID, delivery.StatusRunning); err != nil {
			t.Fatalf("reset %s running after reopen %d: %v", chunkID, i+1, err)
		}
	}
	act, err := reconcileReopenOrFail(ledgerStore, "stack-conf", chunkID)
	if err != nil {
		t.Fatalf("final reconcile for %s: %v", chunkID, err)
	}
	if err := applyReconcileAction(ledgerStore, "stack-conf", act); err != nil {
		t.Fatalf("apply mark_failed for %s: %v", chunkID, err)
	}
}

// confApplyCLIEvents seeds one chunk's scenario events as durable state and
// runs the CLI driver decisions the events call for.
func confApplyCLIEvents(t *testing.T, ledgerStore *workflowledger.Store, repo workflowledger.Repository, probe gitMergeChecker, scenario delivery.StackConformanceScenario, c delivery.ConformanceScenarioChunk) {
	t.Helper()
	canceled, hasOverlap, failed := false, false, false
	for _, ev := range c.Events {
		switch ev {
		case "merged", "pruned-branch":
		case "canceled":
			canceled = true
		case "overlap":
			hasOverlap = true
		case "failed":
			failed = true
		default:
			t.Fatalf("scenario %q: CLI runner does not model event %q", scenario.Name, ev)
		}
	}
	switch {
	case failed:
		confExhaustCLIBudget(t, ledgerStore, c.ID)
	case hasOverlap:
		// The durable in-flight fact: the chunk published its PR.
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusPublished); err != nil {
			t.Fatalf("seed %s published: %v", c.ID, err)
		}
		// The CLI merge path runs the overlap guard before any squash merge
		// (guardChunkMergeOverlap, called from autoMergeOne): an overlapping
		// chunk must be refused and stay published, never merged.
		err := guardChunkMergeOverlap(context.Background(), confFakeGit{overlapFiles: c.Files}, delivery.GitContext{}, "main", "wf/"+c.ID, c.ID)
		if err == nil {
			t.Errorf("chunk %s: overlap guard must refuse a shared-file merge", c.ID)
		}
	case canceled:
		// The durable fact is the canceled chunk task; it is terminal
		// (stacking_plan.go StatusCanceled) and no drive pass may move it.
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusRunning); err != nil {
			t.Fatalf("seed %s running: %v", c.ID, err)
		}
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusCanceled); err != nil {
			t.Fatalf("seed %s canceled: %v", c.ID, err)
		}
	default:
		// The merged/pruned-branch verdict: durable pushed evidence plus the
		// PR oracle must answer merged even when the local ancestor probe is
		// inconclusive and FindByHead cannot resolve the pruned ref.
		runID := confSeedCLIRun(t, repo, c.ID)
		confSeedCLIPushedRecord(t, repo, runID, "wf/"+c.ID)
		merged, err := probe.Merged(context.Background(), "wf/"+c.ID, "main", "cafe1234", "acme/widgets", true)
		if err != nil {
			t.Fatalf("chunk %s merge probe: %v", c.ID, err)
		}
		if !merged {
			t.Fatalf("chunk %s: probe must resolve a squash-merged PR with a pruned branch", c.ID)
		}
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusPublished); err != nil {
			t.Fatalf("seed %s published: %v", c.ID, err)
		}
		if err := ledgerStore.TransitionTask("stack-conf", c.ID, delivery.StatusMerged); err != nil {
			t.Fatalf("merge %s: %v", c.ID, err)
		}
	}
}

// TestStackConformanceCLI runs every CLI-owned catalog scenario through the
// CLI driver's merge-observation and reconcile decisions and asserts the
// shared expected end states.
func TestStackConformanceCLI(t *testing.T) {
	for _, scenario := range delivery.StackConformanceScenarios() {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			if !scenario.ScenarioOwnedBy(delivery.OwnerDriverCLI) && !scenario.ScenarioOwnedBy(delivery.OwnerCLIOnly) {
				t.Fatalf("scenario %q is owned by %v but not wired into this runner", scenario.Name, scenario.Owners)
			}
			var plans []delivery.ChunkPlan
			for _, c := range scenario.Chunks {
				plans = append(plans, delivery.ChunkPlan{ID: c.ID, Title: c.ID, Files: c.Files, DependsOn: c.Deps})
			}
			ledgerStore, repo := confSeedCLIStack(t, plans)
			probe := gitMergeChecker{git: confFakeGit{}, pr: confFakePR{}}

			for _, c := range scenario.Chunks {
				confApplyCLIEvents(t, ledgerStore, repo, probe, scenario, c)
			}

			after, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-conf")
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range scenario.Chunks {
				want := scenario.Expect[c.ID]
				if got := after[c.ID].Status; got != want {
					t.Errorf("chunk %s end status = %q, want %q", c.ID, got, want)
				}
			}
		})
	}
}

// guard: the catalog's no-merge-authority contract - compile-level proof that
// the overlap rows stay CLI-only is the OwnerCLIOnly label; this assertion
// keeps the sentinel wiring intact.
func TestStackConformanceProbeSentinelAlias(t *testing.T) {
	if !errors.Is(errMergeProbeUnavailable, delivery.ErrMergeProbeUnavailable) {
		t.Fatal("errMergeProbeUnavailable must alias the shared delivery sentinel")
	}
}
