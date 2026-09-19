package localengine

import (
	"context"
	"testing"

	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestPrMergedNoPRAdapter pins prMerged's fallback: without a PR adapter the
// probe answers "not merged" and no error, so the stack keeps waiting without
// a network call (coverage repair after the agenttools split).
func TestPrMergedNoPRAdapter(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	snap := workflowledger.RunSnapshot{
		RunID: "wfr-pr-merged", WorkflowName: "wf", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte("name = \"wf\"\n[[steps]]\nid = \"success\"\n"),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, next, nil); err != nil {
			t.Fatal(err)
		}
		cur, err = repo.GetRun(ctx, snap.RunID)
		if err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{WorkspaceRoot: t.TempDir(), Repo: repo}
	merged, err := engine.prMerged(ctx, cur)
	if err != nil || merged {
		t.Fatalf("prMerged(no PR) = %v, %v", merged, err)
	}
}

// prStub is the minimal PRClient: IsMerged always answers false.
type prStub struct{ merged bool }

func (p prStub) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}
func (p prStub) IsMerged(context.Context, string, string) (bool, error) {
	return p.merged, nil
}
func (p prStub) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}

// TestPrMergedGitContextFailureFallsBackToRemote covers the branch where a
// git runner is configured but the run has no resolvable worktree: the local
// ancestor probe is skipped and the remote PR state answers alone.
// TestPrMergedGitContextResolvesAndAsksRemote covers the success path: a real
// worktree resolves a git context, the local ancestor probe answers "not an
// ancestor", and the remote PR state answers merged.
func TestPrMergedGitContextResolvesAndAsksRemote(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	repoRoot, originURL := coverageDeliveryRepo(t)
	baseCommit := runGit(t, repoRoot, "rev-parse", "HEAD")
	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "wt-probe")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-probe", worktreeRoot, baseCommit)
	runGit(t, worktreeRoot, "config", "user.email", "test@example.com")
	runGit(t, worktreeRoot, "config", "user.name", "Test")

	snap := workflowledger.RunSnapshot{
		RunID: "wfr-pr-resolved", WorkflowName: "branch-deliver", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: baseCommit,
		WorktreeName: "wt-probe", RemoteURL: originURL,
		Status: workflowledger.RunStatusPending,
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(branchDeliveryTOML),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "x"},
		Delivery:         &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{
		WorkspaceRoot: repoRoot, Repo: repo,
		Git: delivery.RealGit{}, PR: prStub{merged: true},
	}
	engine.recordWorktree(snap.RunID, Identity{Root: worktreeRoot, MainRoot: repoRoot})
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: snap.RunID, IdempotencyKey: "probe", Status: "pushed",
		HeadRef: "wf/wt-probe", CommitSHA: baseCommit,
	}); err != nil {
		t.Fatal(err)
	}
	if records, err := repo.ListDeliveries(ctx, snap.RunID); err != nil || len(records) == 0 {
		t.Fatalf("ListDeliveries after upsert = %v, %v", records, err)
	}
	gc, gcErr := engine.deliveryGitCtx(ctx, cur)
	if gcErr != nil {
		t.Fatalf("deliveryGitCtx: %v", gcErr)
	}
	_ = gc
	cur.RemoteURL = "https://github.com/owner/repo.git"
	merged, err := engine.prMerged(ctx, cur)
	if err != nil || !merged {
		t.Fatalf("prMerged(resolved context, remote merged) = %v, %v", merged, err)
	}
}

func TestPrMergedGitContextFailureFallsBackToRemote(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	snap := workflowledger.RunSnapshot{
		RunID: "wfr-pr-gitfail", WorkflowName: "wf", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte("name = \"wf\"\n[[steps]]\nid = \"success\"\n"),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{
		WorkspaceRoot: t.TempDir(), Repo: repo,
		Git: delivery.RealGit{}, PR: prStub{},
	}
	merged, err := engine.prMerged(ctx, cur)
	if err != nil || merged {
		t.Fatalf("prMerged(unresolvable git context) = %v, %v; want false, nil", merged, err)
	}
}
