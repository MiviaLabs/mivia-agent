package legacytui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newWorktreeCommandRepo, runGit, writeWorktreeStoreConfig, worktreeStoreConfig,
// and tomlPathLiteral are package-local copies of internal/cli's helpers of
// the same name (worktree_command_test.go, toml_helpers_test.go): Go test
// files are not part of a package's importable surface, so a helper shared
// by tests in both packages must exist in each; internal/cli's staying
// worktree_command_test.go still needs its own copy.

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

// tomlPathLiteral renders a filesystem path as a TOML basic-string literal.
// Windows paths contain backslashes, which TOML parses as escape sequences
// ("\U" in "C:\Users" is not a valid escape), so each backslash must be
// doubled to survive parsing as a literal backslash.
func tomlPathLiteral(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}

// blockedContextRoot is a package-local copy of internal/cli's helper of the
// same name (context_setup_coverage_test.go): a repo root whose context
// store path is blocked by a plain file, for open-error tests.
func blockedContextRoot(t *testing.T) string {
	t.Helper()
	root := newWorktreeCommandRepo(t)
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, root, filepath.Join(blocker, "context.db"))
	return root
}
