package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestExecuteWorktreeCommandIsRegistered(t *testing.T) {
	err := Execute([]string{"worktree"})
	if err == nil || !strings.Contains(err.Error(), "expected create, list, remove, or adopt") {
		t.Fatalf("error = %v, want worktree usage error", err)
	}
}

func TestWorktreeCommandAdoptAddsMarker(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := vcs.Create(context.Background(), repoRoot, "legacy", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatalf("seed legacy route: %v", err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"adopt", "legacy", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !strings.Contains(output.String(), "adopted worktree \"legacy\"") {
		t.Fatalf("adopt output = %q", output.String())
	}
	if _, err := readWorktreeMarker(worktree.Path); err != nil {
		t.Fatalf("read adopted marker: %v", err)
	}
}

func TestWorktreeCommandCreateListRemove(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	storePath := filepath.Join(repoRoot, "repository.db")
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"create", "Feature One", "--branch", "HEAD", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(output.String(), "created worktree \"feature-one\"") {
		t.Fatalf("create output = %q", output.String())
	}

	worktree, err := vcs.Resolve(context.Background(), repoRoot, "feature-one")
	if err != nil || worktree == nil {
		t.Fatalf("resolve created worktree = %v, %v", worktree, err)
	}
	routeStore, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatalf("open route store: %v", err)
	}
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		routeStore.Close()
		t.Fatal(err)
	}
	routes, err := routeStore.ListSessions(context.Background(), principal)
	routeStore.Close()
	if err != nil || len(routes) != 1 || !routes[0].WorktreeRoute || routes[0].Dir != worktree.Path {
		t.Fatalf("worktree route = %+v, err=%v", routes, err)
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"list", "--workspace", worktree.Path}, &output); err != nil {
		t.Fatalf("list from linked worktree: %v", err)
	}
	if !strings.Contains(output.String(), "feature-one\tmivia/feature-one\t"+worktree.Path) {
		t.Fatalf("list output = %q", output.String())
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"remove", "feature-one", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output.String() != "removed worktree \"feature-one\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	worktree, err = vcs.Resolve(context.Background(), repoRoot, "feature-one")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
	routeStore, err = openContextStorePath(storePath)
	if err != nil {
		t.Fatalf("reopen route store: %v", err)
	}
	routes, err = routeStore.ListSessions(context.Background(), principal)
	routeStore.Close()
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after remove = %+v, err=%v", routes, err)
	}
}

func TestWorktreeCommandUsesConfiguredBranchPrefixOutsideWorkspace(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	configPath := filepath.Join(repoRoot, ".mivia", "mivia.toml")
	configText := worktreeStoreConfig("repository.db") + `
[worktrees]
branch_prefix = "team/"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write worktree config: %v", err)
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change outside workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"create", "Prefix Target", "--branch", "HEAD", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("create: %v", err)
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"list", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(output.String(), "prefix-target\tteam/prefix-target\t") {
		t.Fatalf("list output = %q, want configured team/ branch", output.String())
	}
	if strings.Contains(output.String(), "wt/prefix-target") {
		t.Fatalf("list output = %q, must not use default wt/ branch", output.String())
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"remove", "Prefix Target", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output.String() != "removed worktree \"prefix-target\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, "prefix-target")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
}

func TestWorktreeCommandPreservesExistingBranchWhenPrefixChanges(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	configPath := filepath.Join(repoRoot, ".mivia", "mivia.toml")
	configText := worktreeStoreConfig("repository.db") + `
[worktrees]
branch_prefix = "team/"
`
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("make config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write initial worktree config: %v", err)
	}

	if err := runWorktreeWithIO([]string{"create", "Preserved Branch", "--branch", "HEAD", "--workspace", repoRoot}, &bytes.Buffer{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(worktreeStoreConfig("repository.db")), 0o600); err != nil {
		t.Fatalf("change worktree config: %v", err)
	}

	if err := runWorktreeWithIO([]string{"remove", "Preserved Branch", "--workspace", repoRoot}, &bytes.Buffer{}); err != nil {
		t.Fatalf("remove after prefix change: %v", err)
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, "preserved-branch")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
	if err := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/team/preserved-branch").Run(); err != nil {
		t.Fatalf("old branch was removed after prefix change: %v", err)
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
store_path = "` + storePath + `"
`
}

func TestWorktreeCommandRefusesCurrentWorktreeRemoval(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "protected", "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatalf("change to worktree: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	err = runWorktreeWithIO([]string{"remove", "protected", "--workspace", repoRoot}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot remove the current worktree") {
		t.Fatalf("remove current worktree error = %v", err)
	}
}

func TestWorktreeCommandRejectsInvalidFlags(t *testing.T) {
	tests := [][]string{
		{"create", "--unknown"},
		{"create", "feature", "--branch"},
		{"list", "--branch", "HEAD"},
		{"remove", "feature", "--workspace="},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			err := runWorktreeWithIO(args, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("runWorktreeWithIO(%q) succeeds", args)
			}
		})
	}
}

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
