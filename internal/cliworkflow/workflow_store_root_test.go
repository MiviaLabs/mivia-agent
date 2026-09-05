package cliworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// gitRepoWithWorktree builds a real repo plus one linked worktree, since the
// defect only exists when MainRepoRoot(root) != root.
func gitRepoWithWorktree(t *testing.T) (mainRoot, worktreeRoot string) {
	t.Helper()
	base := t.TempDir()
	mainRoot = filepath.Join(base, "main")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	run(mainRoot, "init", "-q", "-b", "main")
	run(mainRoot, "config", "user.email", "t@example.com")
	run(mainRoot, "config", "user.name", "t")
	run(mainRoot, "commit", "-q", "--allow-empty", "-m", "seed")
	worktreeRoot = filepath.Join(base, "wt")
	run(mainRoot, "worktree", "add", "-q", "-b", "side", worktreeRoot)
	// A tracked project config in the worktree is what makes the store
	// resolve there - the shape this repo itself dogfoods.
	if err := os.MkdirAll(filepath.Join(worktreeRoot, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".mivia", "mivia.toml"), []byte("[chat]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return mainRoot, worktreeRoot
}

// A workflow started from a session bound to a linked worktree creates its own
// git worktree under the MAIN repo (vcs.MainRepoRoot, by design - that is where
// worktrees live), but its durable ledger followed the CALLER's root into the
// linked worktree. Removing that worktree then deleted the only record of the
// run, orphaning the wf/ worktree and its branch in the operator's main
// checkout: invisible to workflow_list_runs, never reaped, unrecoverable.
// Project memory already solves exactly this by anchoring to the main checkout
// (canonicalRepoRoot); the run ledger must live in the same lifecycle domain
// as the worktrees it owns.
func TestWorkflowStoreRootAnchorsToTheMainCheckout(t *testing.T) {
	mainRoot, worktreeRoot := gitRepoWithWorktree(t)
	res := &config.Resolved{Subagents: config.DefaultSubagentConfig}

	cfg := workflowToolSubagentConfig(worktreeRoot, res)
	if cfg.StorePath == "" {
		t.Fatal("no store path resolved")
	}
	if strings.HasPrefix(filepath.Clean(cfg.StorePath), filepath.Clean(worktreeRoot)+string(filepath.Separator)) {
		t.Fatalf("workflow store resolved into the linked worktree (%s): removing that worktree destroys the ledger for runs whose git worktrees live under %s",
			cfg.StorePath, mainRoot)
	}
}
