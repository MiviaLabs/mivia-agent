package localengine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// deliverToDevTOML declares a non-"main" delivery target, so admission
// exercises the containment fix against a real cross-branch target name
// instead of the trivial same-branch case.
const deliverToDevTOML = `version = 1
name = "deliver-to-dev"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

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

// TestEngineAdmitsFeatureBranchTargetingDev is the end-to-end regression for
// the bug this fix addresses: a run started from a feature branch forked
// from dev, delivering to dev (a branch other than main), used to fail
// admission whenever dev had moved since the fork - the old admission gate
// required the delivery target's LOCAL ref to sit at the EXACT SAME commit
// as the worktree's base commit, which only ever held when the run happened
// to start from a branch at the target's then-current tip.
//
// Here dev advances locally (unpushed) AFTER feature-x forks from it: under
// the old equality check, admission would refuse with "delivery base "dev"
// is not at the admitted base commit" even though feature-x's fork point is
// perfectly reachable from dev. The new containment check must admit this
// run and record the run's OriginBaseCommit as the FETCHED origin/dev tip
// (still at the fork point, since dev's local advance was never pushed) -
// not feature-x's own branch state.
func TestEngineAdmitsFeatureBranchTargetingDev(t *testing.T) {
	repoRoot := t.TempDir()
	runGitT(t, repoRoot, "init", "-q", "-b", "dev")
	runGitT(t, repoRoot, "config", "user.email", "test@example.com")
	runGitT(t, repoRoot, "config", "user.name", "Test")
	writeFileT(t, filepath.Join(repoRoot, "a.txt"), "base\n")
	runGitT(t, repoRoot, "add", "a.txt")
	runGitT(t, repoRoot, "commit", "-q", "-m", "base")
	forkPoint := runGitOutT(t, repoRoot, "rev-parse", "HEAD")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, filepath.Dir(originDir), "init", "-q", "--bare", filepath.Base(originDir))
	runGitT(t, repoRoot, "remote", "add", "origin", originDir)
	runGitT(t, repoRoot, "push", "-q", "-u", "origin", "dev")

	runGitT(t, repoRoot, "checkout", "-q", "-b", "feature-x")
	runGitT(t, repoRoot, "checkout", "-q", "dev")
	writeFileT(t, filepath.Join(repoRoot, "b.txt"), "dev advanced locally, not pushed\n")
	runGitT(t, repoRoot, "add", "b.txt")
	runGitT(t, repoRoot, "commit", "-q", "-m", "advance dev")
	// feature-x is checked out for admission: workspace.Ensure resolves the
	// base ref/commit from whatever branch is currently checked out.
	runGitT(t, repoRoot, "checkout", "-q", "feature-x")

	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-to-dev.toml"), deliverToDevTOML)

	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-feature-to-dev" },
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "deliver-to-dev", Inputs: map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatalf("Start() = %v, want admission to succeed under the containment check", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.BaseRef != "feature-x" {
		t.Fatalf("BaseRef = %q, want %q", run.BaseRef, "feature-x")
	}
	if run.BaseCommit != forkPoint {
		t.Fatalf("BaseCommit = %q, want the fork point %q", run.BaseCommit, forkPoint)
	}
	if run.OriginBaseCommit != forkPoint {
		t.Fatalf("OriginBaseCommit = %q, want the fetched origin/dev tip %q (dev's local, unpushed advance must not leak in)", run.OriginBaseCommit, forkPoint)
	}
	if run.RemoteURL == "" {
		t.Fatal("RemoteURL must be recorded for a delivery-active admission")
	}
	if _, err := os.Stat(originDir); err != nil {
		t.Fatalf("origin dir missing: %v", err)
	}
}
