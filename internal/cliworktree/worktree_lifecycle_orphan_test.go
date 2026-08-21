//go:build unix

package cliworktree

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

const worktreeLifecycleOrphanHelper = "MIVIA_WORKTREE_LIFECYCLE_ORPHAN_HELPER"

func TestWorktreeLifecycleLockSurvivesRemovingParentExit(t *testing.T) {
	testWorktreeLifecycleLockSurvivesParentExit(t, "remove")
}

// testWorktreeLifecycleLockSurvivesParentExit pins the lock-survival
// contract: killing the Mivia parent while its Git mutation child is blocked
// orphans the child, which keeps the lifecycle lock busy until it exits, so a
// same-name lock is refused and the name cannot be reused mid-mutation. Only
// the "remove" phase is exercised: `git worktree remove` is the last Git
// subprocess in the removal flow. Pruning is no longer a Git subprocess (the
// targeted file-based pruneStaleWorktree replaced the repo-wide `git worktree
// prune`, which dropped intact worktrees), so there is no orphaned Git child
// for a prune phase to hold the lock; the prune-phase variant was removed
// with that behavior.
func testWorktreeLifecycleLockSurvivesParentExit(t *testing.T, phase string) {
	if os.Getenv(worktreeLifecycleOrphanHelper) == "1" {
		runWorktreeLifecycleOrphanHelper(t)
		return
	}

	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := CreateManagedWorktree(repoRoot, "orphan-remove", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	helperDir := t.TempDir()
	readyPath := filepath.Join(helperDir, "ready")
	releasePath := filepath.Join(helperDir, "release")
	donePath := filepath.Join(helperDir, "done")
	writeBlockingGit(t, helperDir)

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testBinary, "-test.run=^"+t.Name()+"$")
	cmd.Env = replaceTestEnv(os.Environ(), map[string]string{
		worktreeLifecycleOrphanHelper: "1",
		"MIVIA_ORPHAN_REPO":           repoRoot,
		"MIVIA_ORPHAN_NAME":           worktree.Name,
		"MIVIA_ORPHAN_REAL_GIT":       realGit,
		"MIVIA_ORPHAN_READY":          readyPath,
		"MIVIA_ORPHAN_RELEASE":        releasePath,
		"MIVIA_ORPHAN_DONE":           donePath,
		"MIVIA_ORPHAN_PHASE":          phase,
		"PATH":                        helperDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			releaseBlockingGit(t, releasePath, donePath)
		}
	}()
	waitForTestFile(t, readyPath, "blocked Git child")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill Mivia parent: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed Mivia parent exits successfully")
	}

	lock, err := LockWorktreeLifecycle(repoRoot, worktree.Name)
	if err == nil {
		lock.Close()
		t.Fatalf("same-name lock succeeds while the orphan Git child is alive; parent output: %s", output.String())
	}
	if !strings.Contains(err.Error(), "lock is busy") {
		t.Fatalf("same-name lock error = %v, want busy lock", err)
	}
	releaseBlockingGit(t, releasePath, donePath)
	released = true
	if err := RunWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &bytes.Buffer{}); err != nil {
		t.Fatalf("recover removal: %v", err)
	}
	replacement, err := CreateManagedWorktree(repoRoot, worktree.Name, "HEAD", "mivia/")
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}
	assertWorktreeLifecycleReplacement(t, repoRoot, replacement)
}

func assertWorktreeLifecycleReplacement(t *testing.T, repoRoot string, replacement *vcs.WorktreeInfo) {
	t.Helper()
	if _, err := os.Stat(replacement.Path); err != nil {
		t.Fatalf("replacement path: %v", err)
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, replacement.Name)
	if err != nil || resolved == nil {
		t.Fatalf("replacement Git registration = %+v, %v", resolved, err)
	}
	assertManagedWorktreeActive(t, repoRoot, replacement)
}

func runWorktreeLifecycleOrphanHelper(t *testing.T) {
	err := RunWorktreeWithIO([]string{
		"remove",
		os.Getenv("MIVIA_ORPHAN_NAME"),
		"--workspace",
		os.Getenv("MIVIA_ORPHAN_REPO"),
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

func writeBlockingGit(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "worktree" ] && [ "$2" = "$MIVIA_ORPHAN_PHASE" ]; then
  printf '%s\n' "$$" > "$MIVIA_ORPHAN_READY"
  while [ ! -e "$MIVIA_ORPHAN_RELEASE" ]; do
    sleep 0.02
  done
  "$MIVIA_ORPHAN_REAL_GIT" "$@"
  status=$?
  : > "$MIVIA_ORPHAN_DONE"
  exit "$status"
fi
exec "$MIVIA_ORPHAN_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func replaceTestEnv(current []string, replacements map[string]string) []string {
	result := make([]string, 0, len(current)+len(replacements))
	for _, entry := range current {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := replacements[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

func waitForTestFile(t *testing.T, path, description string) {
	t.Helper()
	if !testFileAppears(path, 5*time.Second) {
		t.Fatalf("timeout while waiting for %s", description)
	}
}

func releaseBlockingGit(t *testing.T, releasePath, donePath string) {
	t.Helper()
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Errorf("release Git child: %v", err)
		return
	}
	if !testFileAppears(donePath, 5*time.Second) {
		t.Error("timeout while waiting for Git child exit")
	}
}

func testFileAppears(path string, timeout time.Duration) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return false
		}
	}
}
