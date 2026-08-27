package clichat

// Package-local copies of internal/cliworktree's worktree_command_test.go
// test helpers (newWorktreeCommandRepo, writeWorktreeStoreConfig, runGit,
// worktreeStoreConfig): 68 call sites across chat_command_worktree_test.go,
// chat_worktree_coverage_test.go, context_setup_coverage_test.go,
// runtime_paths_coverage_test.go, and this package's other worktree-flavored
// tests need these, but those files import internal/chat and stay in
// internal/cli for now (clichat has not been extracted yet), while the
// worktree-command production code and its tests moved to
// internal/cliworktree. Go does not allow importing another package's
// _test.go symbols, so the helpers are duplicated here rather than shared -
// same pattern the codebase already uses for internal/legacytui's
// package-local copies (see session_test_helpers_test.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newWorktreeCommandRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	path := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "initial")
	return repoRoot
}

// runGit runs a git command in the given directory. Fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", cmd...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func writeWorktreeStoreConfig(t *testing.T, root, storePath string) {
	t.Helper()
	configDir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configText := worktreeStoreConfig(storePath)
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
}

func worktreeStoreConfig(storePath string) string {
	return `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "` + tomlPathLiteral(storePath) + `"
`
}
