package localengine_test

// Admission-regression tests for the fresh-start identity path:
// worktree cleanup on a failed delivery-active admission (F2), the
// fail-closed refusal when a delivery-active run cannot get a real worktree
// (F5), and admission against the effective delivery base (pr_base) instead
// of the declared one (F6).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// createRunFailingRepo wraps a real repository so CreateRun always fails with
// a non-duplicate, non-conflict, non-claim error. It is how a test forces
// controller.StartNew to fail after a successful pinNewRunIdentity, without
// touching the delivery package's RealGit on the admission path.
type createRunFailingRepo struct {
	workflowledger.Repository
	fail error
}

func (f *createRunFailingRepo) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error {
	return f.fail
}

// TestEngineStartNewFailureRemovesFreshWorktree pins that a StartNew failure
// AFTER a successful pinNewRunIdentity no longer leaks the pre-created run
// worktree. pinNewRunIdentity creates (or validates) the run worktree and
// removes it on its OWN internal error branches only; a later
// ctrl.SetAdmission / ctrl.StartNew(error) returned straight out of startNew.
// The generated worktree for the failed Start was registered and left on disk.
func TestEngineStartNewFailureRemovesFreshWorktree(t *testing.T) {
	repoRoot := newLocalRepoNoOrigin(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)

	real := workflowledger.NewMemoryRepository()
	repo := &createRunFailingRepo{Repository: real, fail: errors.New("storage backend down (forced)")}
	engine := newAdmissionTestEngine(repoRoot, repo, "wfr-startnew-leak")

	_, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "build"},
	})
	if err == nil || !strings.Contains(err.Error(), "storage backend down (forced)") {
		t.Fatalf("Start error = %v, want the forced CreateRun failure", err)
	}
	if _, getErr := real.GetRun(context.Background(), "wfr-startnew-leak"); !errors.Is(getErr, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after the failed StartNew = %v, want ErrNotFound (no run admitted)", getErr)
	}
	assertNoRunWorktree(t, repoRoot, "wfr-startnew-leak")
}

// deliverToDevPRBaseTOML is the cross-branch delivery workflow (base dev)
// with the reserved pr_base name declared as an ordinary string input, so an
// engine start can carry pr_base through admission and the effective-base
// resolution can be exercised.
const deliverToDevPRBaseTOML = `version = 1
name = "deliver-to-dev"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[inputs.pr_base]
type = "string"

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "dev"

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

// TestEngineStartRejectsDeliveryWithoutOriginLeavesNoWorktree pins that a
// failed delivery-active admission cleans up the git worktree the start had
// already created. Before this fix, ensureRunWorktree succeeded (worktree
// created) and the later origin failure returned its error without removing
// the worktree, leaking <root>/.mivia/worktrees/workflow-wfr-* and its
// registration forever.
func TestEngineStartRejectsDeliveryWithoutOriginLeavesNoWorktree(t *testing.T) {
	repoRoot := newLocalRepoNoOrigin(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-me.toml"), deliverMeTOML)

	repo := workflowledger.NewMemoryRepository()
	engine := newAdmissionTestEngine(repoRoot, repo, "wfr-admission-leak")
	_, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "deliver-me", Inputs: map[string]any{"task": "build"},
	})
	if err == nil {
		t.Fatal("expected Start to fail without an origin remote")
	}
	if !strings.Contains(err.Error(), "origin remote") {
		t.Fatalf("error = %q, want origin remote mention", err.Error())
	}
	if _, getErr := repo.GetRun(context.Background(), "wfr-admission-leak"); !errors.Is(getErr, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after the failed Start = %v, want ErrNotFound (no run admitted)", getErr)
	}
	assertNoRunWorktree(t, repoRoot, "wfr-admission-leak")
}

// TestEngineDeliveryActiveRefusesWithoutRunWorktree pins the fail-closed
// guard in the local-identity fallback: a delivery-active workflow must never
// admit through the fabricated no-worktree fallback (resolveLocalIdentity
// returns WorktreeName "workflow-<runID>" with no real worktree), because
// such a run burns its whole body and then permanently refuses at delivery.
// When worktree creation fails but the workspace is still a git repository,
// Start must refuse. A non-delivery workflow must still fall back and admit
// as before.
func TestEngineDeliveryActiveRefusesWithoutRunWorktree(t *testing.T) {
	repoRoot := newLocalRepoNoOrigin(t)

	t.Run("delivery-active workflow refuses without a run worktree", func(t *testing.T) {
		// Block worktree creation for this run: a pre-existing directory at
		// .mivia/worktrees/workflow-wfr-deliver-no-wt makes
		// vcs.CreateWithPrefix return WorktreeExistsError, so ensureRunWorktree
		// reports ok=false and admission reaches the local-identity fallback.
		blockDir := filepath.Join(repoRoot, ".mivia", "worktrees", "workflow-wfr-deliver-no-wt")
		if err := os.MkdirAll(blockDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-me.toml"), deliverMeTOML)

		repo := workflowledger.NewMemoryRepository()
		engine := newAdmissionTestEngine(repoRoot, repo, "wfr-deliver-no-wt")
		_, err := engine.Start(context.Background(), agenttools.StartRequest{
			Workflow: "deliver-me", Inputs: map[string]any{"task": "build"},
		})
		if err == nil || !strings.Contains(err.Error(), "delivery-active workflow cannot admit without a run worktree") {
			t.Fatalf("Start error = %v, want the no-worktree delivery-active refusal", err)
		}
		if _, getErr := repo.GetRun(context.Background(), "wfr-deliver-no-wt"); !errors.Is(getErr, workflowledger.ErrNotFound) {
			t.Fatalf("GetRun after the refused Start = %v, want ErrNotFound", getErr)
		}
	})

	t.Run("non-delivery workflow still falls back and admits", func(t *testing.T) {
		blockDir := filepath.Join(repoRoot, ".mivia", "worktrees", "workflow-wfr-plain-no-wt")
		if err := os.MkdirAll(blockDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)

		repo := workflowledger.NewMemoryRepository()
		engine := newAdmissionTestEngine(repoRoot, repo, "wfr-plain-no-wt")
		started, err := engine.Start(context.Background(), agenttools.StartRequest{
			Workflow: "two-step", Inputs: map[string]any{"task": "x"},
		})
		if err != nil {
			t.Fatalf("non-delivery Start must fall back and admit: %v", err)
		}
		waitRunSettled(t, engine, started.RunID)
		run, err := repo.GetRun(context.Background(), started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusSucceeded {
			t.Fatalf("run status = %q, want succeeded", run.Status)
		}
	})
}

// TestEngineAdmissionHonorsPRBase pins that admission keys off the effective
// delivery base - the pr_base input when a valid one is present, otherwise
// the workflow's declared base - so the origin-containment guard admits
// against the branch delivery will actually publish to. The worktree starts
// from a commit contained in origin/main but not in the declared base dev:
// with pr_base=main the run must admit and record origin/main's tip; without
// pr_base the declared base dev must refuse (the commit is not an ancestor).
func TestEngineAdmissionHonorsPRBase(t *testing.T) {
	repoRoot, mainTip := newPRBaseFixture(t)
	t.Run("pr_base overrides the declared base at admission", func(t *testing.T) {
		repo := workflowledger.NewMemoryRepository()
		engine := newAdmissionTestEngine(repoRoot, repo, "wfr-pr-base")
		started, err := engine.Start(context.Background(), agenttools.StartRequest{
			Workflow: "deliver-to-dev", Inputs: map[string]any{"task": "x", "pr_base": "main"},
		})
		if err != nil {
			t.Fatalf("Start with pr_base=main must admit against origin/main: %v", err)
		}
		waitRunSettled(t, engine, started.RunID)
		run, err := repo.GetRun(context.Background(), started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.OriginBaseCommit != mainTip {
			t.Fatalf("OriginBaseCommit = %q, want the origin/main tip %q (pr_base must govern, not the declared base dev)", run.OriginBaseCommit, mainTip)
		}
		if run.RemoteURL == "" {
			t.Fatal("RemoteURL must be recorded for a delivery-active admission")
		}
	})

	t.Run("declared base still governs without pr_base", func(t *testing.T) {
		repo := workflowledger.NewMemoryRepository()
		engine := newAdmissionTestEngine(repoRoot, repo, "wfr-declared-dev")
		_, err := engine.Start(context.Background(), agenttools.StartRequest{
			Workflow: "deliver-to-dev", Inputs: map[string]any{"task": "x"},
		})
		if err == nil {
			t.Fatal("Start without pr_base must refuse: the worktree base commit is not contained in the declared base dev")
		}
		if !strings.Contains(err.Error(), "does not contain the commit this run started from") {
			t.Fatalf("Start error = %v, want the containment refusal naming the declared base", err)
		}
	})

	t.Run("invalid pr_base falls back to the declared base", func(t *testing.T) {
		// A malformed pr_base must never override the declared base at
		// admission: delivery.EffectiveBase rejects it and returns the declared
		// base dev, so the origin-containment guard still refuses (the worktree
		// base commit is not contained in dev, only in main).
		repo := workflowledger.NewMemoryRepository()
		engine := newAdmissionTestEngine(repoRoot, repo, "wfr-invalid-prbase")
		_, err := engine.Start(context.Background(), agenttools.StartRequest{
			Workflow: "deliver-to-dev", Inputs: map[string]any{"task": "x", "pr_base": "-bad/name"},
		})
		if err == nil {
			t.Fatal("Start with an invalid pr_base must refuse via the declared base dev")
		}
		if !strings.Contains(err.Error(), "does not contain the commit this run started from") {
			t.Fatalf("Start error = %v, want the containment refusal (invalid pr_base must not override)", err)
		}
	})
}

// newPRBaseFixture builds a repo where the run's worktree base commit is
// contained in origin/main but NOT in the declared base dev: dev forks at the
// base commit, main advances beyond it, and a feature branch starts from
// main's tip. It returns the repo root and main's tip SHA.
func newPRBaseFixture(t *testing.T) (repoRoot, mainTip string) {
	t.Helper()
	repoRoot = t.TempDir()
	runGitT(t, repoRoot, "init", "-q", "-b", "main")
	runGitT(t, repoRoot, "config", "user.email", "test@example.com")
	runGitT(t, repoRoot, "config", "user.name", "Test")
	writeFileT(t, filepath.Join(repoRoot, "a.txt"), "base\n")
	runGitT(t, repoRoot, "add", "a.txt")
	runGitT(t, repoRoot, "commit", "-q", "-m", "base")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, filepath.Dir(originDir), "init", "-q", "--bare", filepath.Base(originDir))
	runGitT(t, repoRoot, "remote", "add", "origin", originDir)
	runGitT(t, repoRoot, "push", "-q", "-u", "origin", "main")

	runGitT(t, repoRoot, "checkout", "-q", "-b", "dev")
	runGitT(t, repoRoot, "push", "-q", "-u", "origin", "dev")
	runGitT(t, repoRoot, "checkout", "-q", "main")
	writeFileT(t, filepath.Join(repoRoot, "b.txt"), "main advanced\n")
	runGitT(t, repoRoot, "add", "b.txt")
	runGitT(t, repoRoot, "commit", "-q", "-m", "advance main")
	mainTip = runGitOutT(t, repoRoot, "rev-parse", "HEAD")
	runGitT(t, repoRoot, "push", "-q", "origin", "main")
	runGitT(t, repoRoot, "checkout", "-q", "-b", "feature-pr-base")

	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-to-dev.toml"), deliverToDevPRBaseTOML)
	return repoRoot, mainTip
}

// newAdmissionTestEngine builds the standard static-step engine for the
// admission-regression tests.
func newAdmissionTestEngine(repoRoot string, repo workflowledger.Repository, runID string) *localengine.Engine {
	return &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return runID },
	}
}

// waitRunSettled waits for the background run to exit within the test budget.
func waitRunSettled(t *testing.T, engine *localengine.Engine, runID string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, runID); err != nil {
		t.Fatal(err)
	}
}

// assertNoRunWorktree fails when a worktree named "workflow-<runID>" is still
// registered or still present on disk under the workspace worktrees directory.
func assertNoRunWorktree(t *testing.T, repoRoot, runID string) {
	t.Helper()
	ctx := context.Background()
	worktrees, err := vcs.List(ctx, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := "workflow-" + runID
	for _, wt := range worktrees {
		if wt.Name == want {
			t.Fatalf("worktree %q remains registered after the failed admission (leak)", wt.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".mivia", "worktrees", want)); err == nil {
		t.Fatalf("worktree directory .mivia/worktrees/%s still exists after the failed admission", want)
	}
}
