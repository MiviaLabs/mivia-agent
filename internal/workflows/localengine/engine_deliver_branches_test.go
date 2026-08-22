package localengine

// engine_deliver_branches_test.go covers the early-return and settle-error
// branches of Engine.Deliver and its helpers in engine_deliver.go: engine
// guards, unknown and unreadable runs, wrong statuses, corrupt snapshots and
// definitions, in-flight serialization, claim failures, and the error tails
// of publishDelivery and routeDeliveryRepair.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// branchDeliveryTOML declares a delivery policy with an on_failure repair
// step, so a repairable delivery failure routes back into the workflow.
const branchDeliveryTOML = `version = 1
name = "branch-deliver"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
on_failure = "one"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`

// branchRepo wraps a ledger repository and fails selected calls on demand.
// The flags let each test break exactly one step of the deliver flow.
type branchRepo struct {
	workflowledger.Repository
	getRunErr                     error
	failGetRunSnapshot            bool
	failClaimRun                  bool
	failListAttempts              bool
	failGetRunAfterUpsertDelivery bool
	failGetRunAfterOutcome        bool
	deliveryUpserted              bool
	outcomeRecorded               bool
}

func (r *branchRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	if r.getRunErr != nil {
		return workflowledger.RunSnapshot{}, r.getRunErr
	}
	if (r.failGetRunAfterUpsertDelivery && r.deliveryUpserted) || (r.failGetRunAfterOutcome && r.outcomeRecorded) {
		return workflowledger.RunSnapshot{}, errors.New("get run refused by test")
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *branchRepo) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	if r.failGetRunSnapshot {
		return nil, errors.New("get run snapshot refused by test")
	}
	return r.Repository.GetRunSnapshot(ctx, runID)
}

func (r *branchRepo) ClaimRun(ctx context.Context, runID, holder string) error {
	if r.failClaimRun {
		return errors.New("claim run refused by test")
	}
	return r.Repository.ClaimRun(ctx, runID, holder)
}

func (r *branchRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	if r.failListAttempts {
		return nil, errors.New("list attempts refused by test")
	}
	return r.Repository.ListStepAttempts(ctx, runID)
}

func (r *branchRepo) UpsertDelivery(ctx context.Context, d workflowledger.DeliveryRecord) error {
	if err := r.Repository.UpsertDelivery(ctx, d); err != nil {
		return err
	}
	r.deliveryUpserted = true
	return nil
}

func (r *branchRepo) RecordStepAttemptOutcome(ctx context.Context, attempt workflowledger.StepAttempt, outcome workflowledger.AttemptOutcome) error {
	if err := r.Repository.RecordStepAttemptOutcome(ctx, attempt, outcome); err != nil {
		return err
	}
	r.outcomeRecorded = true
	return nil
}

// branchSeedPendingRun stores a delivery_pending run carrying the given
// definition TOML and raw snapshot bytes.
func branchSeedPendingRun(t *testing.T, repo workflowledger.Repository, runID, defTOML string, raw []byte) workflowledger.RunSnapshot {
	t.Helper()
	ctx := context.Background()
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "branch-deliver", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		cur, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if cur.Status == next && next == workflowledger.RunStatusDeliveryPending {
			return cur
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, next, nil); err != nil {
			t.Fatalf("CAS to %s: %v", next, err)
		}
	}
	cur, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("final GetRun: %v", err)
	}
	return cur
}

// branchSnapshot builds one canonical snapshot for the given definition.
func branchSnapshot(t *testing.T, defTOML string, defDigest string) []byte {
	t.Helper()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(defTOML),
		DefinitionDigest: defDigest,
		Inputs:           map[string]string{"task": "x"},
		Delivery:         &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"},
	})
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	return raw
}

// branchGitRun seeds a real git worktree run (main repo, bare origin,
// worktree with an uncommitted change) for the full publish path.
func branchGitRun(t *testing.T, runID string, repo workflowledger.Repository) (repoRoot string) {
	t.Helper()
	repoRoot, originURL := coverageDeliveryRepo(t)
	baseCommit := runGit(t, repoRoot, "rev-parse", "HEAD")
	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "wt-branch")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-branch", worktreeRoot, baseCommit)
	runGit(t, worktreeRoot, "config", "user.email", "test@example.com")
	runGit(t, worktreeRoot, "config", "user.name", "Test")
	coverageWriteFile(t, filepath.Join(worktreeRoot, "a.txt"), "base\nchanged\n")

	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "branch-deliver", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: baseCommit,
		WorktreeName: "wt-branch", RemoteURL: originURL,
		Status: workflowledger.RunStatusPending,
	}
	ctx := context.Background()
	if err := repo.CreateRun(ctx, snap, branchSnapshot(t, branchDeliveryTOML, "digest")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		cur, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, next, nil); err != nil {
			t.Fatalf("CAS to %s: %v", next, err)
		}
	}
	return repoRoot
}

func TestDeliverIncompleteEngineErrors(t *testing.T) {
	var nilEngine *Engine
	if _, err := nilEngine.Deliver(context.Background(), "r", true); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("nil engine Deliver error = %v", nilEngine)
	}
	e := &Engine{}
	if _, err := e.Deliver(context.Background(), "r", true); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("engine without repo Deliver error = %v", nilEngine)
	}
}

func TestDeliverUnknownRunErrors(t *testing.T) {
	e := &Engine{Repo: workflowledger.NewMemoryRepository()}
	_, err := e.Deliver(context.Background(), "wfr-missing", true)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Deliver unknown run error = %v", err)
	}
}

func TestDeliverGetRunErrorPropagates(t *testing.T) {
	wrapped := &branchRepo{Repository: workflowledger.NewMemoryRepository(), getRunErr: errors.New("ledger down")}
	e := &Engine{Repo: wrapped}
	_, err := e.Deliver(context.Background(), "wfr-any", true)
	if err == nil || !strings.Contains(err.Error(), "ledger down") {
		t.Fatalf("Deliver GetRun error = %v", err)
	}
}

func TestDeliverWrongStatusErrors(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	snap := workflowledger.RunSnapshot{
		RunID: "wfr-running", WorkflowName: "branch-deliver", WorkflowDigest: "digest",
		ActiveStepID: "one", BaseRef: "main", BaseCommit: "deadbeef",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, branchSnapshot(t, branchDeliveryTOML, "digest")); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Repo: repo}
	_, err = e.Deliver(ctx, snap.RunID, true)
	if err == nil || !strings.Contains(err.Error(), "not waiting for delivery") {
		t.Fatalf("Deliver wrong status error = %v", err)
	}
}

func TestDeliverGetRunSnapshotErrorPropagates(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, inner, "wfr-nosnap", branchDeliveryTOML, branchSnapshot(t, branchDeliveryTOML, "digest"))
	e := &Engine{Repo: &branchRepo{Repository: inner, failGetRunSnapshot: true}}
	if _, err := e.Deliver(context.Background(), "wfr-nosnap", true); err == nil || !strings.Contains(err.Error(), "get run snapshot refused") {
		t.Fatalf("Deliver GetRunSnapshot error = %v", err)
	}
}

func TestDeliverCorruptSnapshotJSONErrors(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, repo, "wfr-badjson", branchDeliveryTOML, []byte("not json at all"))
	e := &Engine{Repo: repo}
	if _, err := e.Deliver(context.Background(), "wfr-badjson", true); err == nil {
		t.Fatal("Deliver with a corrupt snapshot JSON must error")
	}
}

func TestDeliverInvalidDefinitionTOMLErrors(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, repo, "wfr-badtoml", branchDeliveryTOML, branchSnapshot(t, "not = valid workflow", "digest"))
	e := &Engine{Repo: repo}
	if _, err := e.Deliver(context.Background(), "wfr-badtoml", true); err == nil {
		t.Fatal("Deliver with an invalid definition TOML must error")
	}
}

func TestDeliverOnFailureTargetMissingFailsCompile(t *testing.T) {
	// ParseWorkflowTOML does not resolve step on_failure targets, so this
	// definition parses but fails CompileForResume with an on_failure error.
	broken := `version = 1
name = "branch-deliver"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "missing-step"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	repo := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, repo, "wfr-badcompile", broken, branchSnapshot(t, broken, "digest"))
	e := &Engine{Repo: repo}
	_, err := e.Deliver(context.Background(), "wfr-badcompile", true)
	if err == nil || !strings.Contains(err.Error(), "on_failure") {
		t.Fatalf("Deliver compile error = %v, want the on_failure target error", err)
	}
}

func TestDeliverAlreadyInProgressRefuses(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, repo, "wfr-busy", branchDeliveryTOML, branchSnapshot(t, branchDeliveryTOML, "digest"))
	e := &Engine{Repo: repo, delivering: map[string]string{"wfr-busy": "holder-x"}}
	_, err := e.Deliver(context.Background(), "wfr-busy", true)
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("Deliver busy error = %v", err)
	}
}

func TestDeliverClaimFailurePropagates(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	branchSeedPendingRun(t, inner, "wfr-claim", branchDeliveryTOML, branchSnapshot(t, branchDeliveryTOML, "digest"))
	e := &Engine{Repo: &branchRepo{Repository: inner, failClaimRun: true}}
	_, err := e.Deliver(context.Background(), "wfr-claim", true)
	if err == nil || !strings.Contains(err.Error(), "claim run refused") {
		t.Fatalf("Deliver claim error = %v", err)
	}
}

func TestDeliverVerifyGitDirFailurePropagates(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	repoRoot := branchGitRun(t, "wfr-badgit", repo)
	e := &Engine{WorkspaceRoot: repoRoot, Repo: repo, PR: &coverageRecordingPR{}}
	// Record the worktree identity so delivery skips Resolve, then corrupt
	// the worktree's .git file: VerifyGitDir must fail with a plain error.
	e.recordWorktree("wfr-badgit", Identity{
		Root:     filepath.Join(repoRoot, ".mivia", "worktrees", "wt-branch"),
		MainRoot: repoRoot,
	})
	dotGit := filepath.Join(repoRoot, ".mivia", "worktrees", "wt-branch", ".git")
	if err := os.WriteFile(dotGit, []byte("gitdir: /bad/path"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.Deliver(context.Background(), "wfr-badgit", true)
	if err == nil || !strings.Contains(err.Error(), "verify git dir") {
		t.Fatalf("Deliver verify git dir error = %v", err)
	}
}

func TestDeliverSettleSucceededGetRunErrorPropagates(t *testing.T) {
	wrapped := &branchRepo{Repository: workflowledger.NewMemoryRepository(), failGetRunAfterUpsertDelivery: true}
	repoRoot := branchGitRun(t, "wfr-settle", wrapped)
	e := &Engine{WorkspaceRoot: repoRoot, Repo: wrapped, PR: &coverageRecordingPR{}}
	_, err := e.Deliver(context.Background(), "wfr-settle", true)
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("Deliver settle GetRun error = %v", err)
	}
}

func TestDeliverRepairListAttemptsErrorPropagates(t *testing.T) {
	wrapped := &branchRepo{Repository: workflowledger.NewMemoryRepository(), failListAttempts: true}
	repoRoot := branchGitRun(t, "wfr-repair-list", wrapped)
	e := &Engine{
		WorkspaceRoot: repoRoot, Repo: wrapped,
		PR: &coverageRecordingPR{failCreate: errors.New("pr create exploded")},
	}
	_, err := e.Deliver(context.Background(), "wfr-repair-list", true)
	if err == nil || !strings.Contains(err.Error(), "list attempts for repair") {
		t.Fatalf("Deliver repair list attempts error = %v", err)
	}
}

func TestDeliverRepairGetRunErrorPropagates(t *testing.T) {
	wrapped := &branchRepo{Repository: workflowledger.NewMemoryRepository(), failGetRunAfterOutcome: true}
	repoRoot := branchGitRun(t, "wfr-repair-getrun", wrapped)
	e := &Engine{
		WorkspaceRoot: repoRoot, Repo: wrapped,
		PR: &coverageRecordingPR{failCreate: errors.New("pr create exploded")},
	}
	_, err := e.Deliver(context.Background(), "wfr-repair-getrun", true)
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("Deliver repair GetRun error = %v", err)
	}
}
