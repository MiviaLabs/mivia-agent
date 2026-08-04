package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// skipIfNoGit skips the test if git is not available.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// initTestRepo creates a temporary git repository with an initial commit.
func initTestRepo(t *testing.T) string {
	t.Helper()
	skipIfNoGit(t)
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	write(t, dir, "README.md", "hello")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "init")
	return dir
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreate(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "feature-auth", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wt.Name != "feature-auth" {
		t.Errorf("Name = %q, want %q", wt.Name, "feature-auth")
	}
	if !filepath.IsAbs(wt.Path) {
		t.Errorf("Path = %q, want absolute", wt.Path)
	}
	if wt.Branch == "" {
		t.Errorf("Branch is empty")
	}
	// Verify the path exists.
	if _, err := os.Stat(wt.Path); err != nil {
		t.Errorf("worktree path does not exist: %v", err)
	}
}

func TestCreate_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, err := Create(ctx, dir, "test", "HEAD")
	if _, ok := err.(NotGitRepoError); !ok {
		t.Errorf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "dup", "HEAD"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := Create(ctx, root, "dup", "HEAD")
	if _, ok := err.(WorktreeExistsError); !ok {
		t.Errorf("expected WorktreeExistsError, got %T: %v", err, err)
	}
}

func TestRemove(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "remove-me", "HEAD"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := Remove(ctx, root, "remove-me"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Path should be gone.
	wtDir := filepath.Join(root, ".mivia", "worktrees", "remove-me")
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists")
	}
}

func TestList(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "wt-one", "HEAD"); err != nil {
		t.Fatalf("create one: %v", err)
	}
	if _, err := Create(ctx, root, "wt-two", "HEAD"); err != nil {
		t.Fatalf("create two: %v", err)
	}
	list, err := List(ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len(list) = %d, want 2", len(list))
	}
	// Verify main tree is not included.
	for _, wt := range list {
		if wt.Name == "" {
			t.Errorf("unexpected empty name (main tree leak)")
		}
	}
}

func TestRepoRoot(t *testing.T) {
	root := initTestRepo(t)
	found, err := RepoRoot(root)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	abs, _ := filepath.Abs(root)
	if found != abs {
		t.Errorf("RepoRoot = %q, want %q", found, abs)
	}
}

func TestRepoRoot_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := RepoRoot(dir)
	if _, ok := err.(NotGitRepoError); !ok {
		t.Errorf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

// TestNoShellPatterns ensures vcs/ never uses shell execution for git.
func TestNoShellPatterns(t *testing.T) {
	root := repoRootForVCS(t)
	// Match shell patterns: exec.CommandContext with "sh", "bash", or shell metacharacters.
	shell := regexp.MustCompile(`exec\.Command[^C].*"sh"|exec\.Command[^C].*"bash"|exec\.Command\("git"[^)]*-c`)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if shell.MatchString(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("shell patterns in vcs/:\n%s", strings.Join(offenders, "\n"))
	}
}

func repoRootForVCS(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // internal/vcs -> repo root
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}
