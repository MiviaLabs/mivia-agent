package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestExecuteWorktreeCommandIsRegistered(t *testing.T) {
	err := Execute([]string{"worktree"})
	if err == nil || !strings.Contains(err.Error(), "expected create, list, or remove") {
		t.Fatalf("error = %v, want worktree usage error", err)
	}
}

func TestWorktreeCommandCreateListRemove(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
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
	routeStore, err := openContextStore(repoRoot, config.DefaultSubagentConfig)
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
	if !strings.Contains(output.String(), "feature-one\twt/feature-one\t"+worktree.Path) {
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
	routeStore, err = openContextStore(repoRoot, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatalf("reopen route store: %v", err)
	}
	routes, err = routeStore.ListSessions(context.Background(), principal)
	routeStore.Close()
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after remove = %+v, err=%v", routes, err)
	}
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
