package vcs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file cover the error and edge branches of the vcs
// package that the diff-coverage gate requires: typed error messages,
// validation failures, git command failures, and parser edge cases.

func TestErrorMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{NotGitRepoError{Dir: "/x"}, "not a git repository: /x"},
		{WorktreeExistsError{Name: "wt-a"}, `worktree "wt-a" already exists`},
		{WorktreeNotFoundError{Name: "wt-a"}, `worktree "wt-a" not found`},
		{InvalidNameError{Input: "..", Reason: "name is reserved"}, "invalid worktree name: name is reserved"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

func TestCreateEmptyName(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	_, err := Create(ctx, root, "", "HEAD")
	if _, ok := err.(InvalidNameError); !ok {
		t.Fatalf("expected InvalidNameError, got %T: %v", err, err)
	}
}

func TestCreateNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, err := Create(ctx, dir, "wt-a", "HEAD")
	if _, ok := err.(NotGitRepoError); !ok {
		t.Fatalf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

// TestCreateInvalidBaseRef drives the git worktree add failure branch: the
// branch cannot be created from a ref that does not exist.
func TestCreateInvalidBaseRef(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	_, err := Create(ctx, root, "wt-a", "no-such-ref")
	if _, ok := err.(*gitCommandError); !ok {
		t.Fatalf("expected gitCommandError, got %T: %v", err, err)
	}
	// Nothing must have been created on disk.
	wtPath := filepath.Join(root, ".mivia", "worktrees", "wt-a")
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir exists after failed create: %v", err)
	}
}

func TestRemoveNotFound(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	err := Remove(ctx, root, "missing")
	if _, ok := err.(WorktreeNotFoundError); !ok {
		t.Fatalf("expected WorktreeNotFoundError, got %T: %v", err, err)
	}
}

func TestRemoveNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	err := Remove(ctx, dir, "wt-a")
	if _, ok := err.(NotGitRepoError); !ok {
		t.Fatalf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

func TestListNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, err := List(ctx, dir)
	if _, ok := err.(NotGitRepoError); !ok {
		t.Fatalf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

func TestMainRepoRootNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := MainRepoRoot(dir)
	if _, ok := err.(NotGitRepoError); !ok {
		t.Fatalf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

func TestCurrentWorktreeNameNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, err := CurrentWorktreeName(ctx, dir)
	if _, ok := err.(NotGitRepoError); !ok {
		t.Fatalf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

// TestResolveGitDirNonGitdirFile covers the final fallback of resolveGitDir:
// a .git file whose content is not a gitdir: pointer is returned unchanged.
func TestResolveGitDirNonGitdirFile(t *testing.T) {
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("not a gitdir line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveGitDir(gitFile)
	abs, _ := filepath.Abs(gitFile)
	if got != abs {
		t.Errorf("resolveGitDir(non-gitdir file) = %q, want %q", got, abs)
	}
}

func TestGitCommandErrorMessage(t *testing.T) {
	withOut := &gitCommandError{cmd: "worktree add", output: "  fatal: boom\n", err: os.ErrInvalid}
	if got := withOut.Error(); !strings.Contains(got, "fatal: boom") || !strings.Contains(got, "worktree add") {
		t.Errorf("Error() with output = %q", got)
	}
	noOut := &gitCommandError{cmd: "worktree list", err: os.ErrInvalid}
	if got := noOut.Error(); strings.Contains(got, ": :") {
		t.Errorf("Error() without output = %q", got)
	}
}

func TestParseWorktreeListEdgeCases(t *testing.T) {
	// A block whose path is outside the mivia worktrees prefix is filtered.
	out := `worktree /other/repo
HEAD 1234567
branch refs/heads/main

worktree /repo/.mivia/worktrees/wt-a
HEAD 89abcde
branch refs/heads/wt/wt-a

worktree /repo/.mivia/worktrees/wt-detached
HEAD 1234567

malformed line without a space
`
	prefix := "/repo/.mivia/worktrees/"
	got, err := parseWorktreeList(out, prefix)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (main and detached blocks outside prefix are filtered)", len(got))
	}
	if got[0].Name != "wt-a" || got[0].Branch != "wt/wt-a" {
		t.Errorf("first = %+v, want wt-a on wt/wt-a", got[0])
	}
	if got[1].Name != "wt-detached" || got[1].Branch != "" {
		t.Errorf("second = %+v, want wt-detached with empty branch", got[1])
	}
}

// TestResolveFoundAndNotFound drives Resolve's happy paths: it finds a
// mivia-managed worktree by name and returns nil, nil when absent.
func TestResolveFoundAndNotFound(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "wt-a", "HEAD"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := Resolve(ctx, root, "wt-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil || got.Name != "wt-a" {
		t.Fatalf("Resolve(wt-a) = %+v, want the worktree", got)
	}
	got, err = Resolve(ctx, root, "missing")
	if err != nil {
		t.Fatalf("Resolve(missing): %v", err)
	}
	if got != nil {
		t.Fatalf("Resolve(missing) = %+v, want nil", got)
	}
}

// TestResolveNotGitRepo covers Resolve's error propagation when the
// underlying List fails on a non-repo directory.
func TestResolveNotGitRepo(t *testing.T) {
	if _, err := Resolve(context.Background(), t.TempDir(), "wt-a"); err == nil {
		t.Fatal("expected an error for a non-repo directory")
	}
}

// TestMainWorktreeFromListing covers mainWorktreeFromListing directly: the
// first worktree line wins and a listing without one yields NotGitRepoError.
func TestMainWorktreeFromListing(t *testing.T) {
	got, err := mainWorktreeFromListing("worktree /repo\nbranch refs/heads/main\n", "")
	if err != nil || got != "/repo" {
		t.Fatalf("mainWorktreeFromListing = %q, %v; want /repo", got, err)
	}
	if _, err := mainWorktreeFromListing("bare\n", "/nowhere"); err == nil {
		t.Fatal("expected NotGitRepoError for a listing without a worktree line")
	}
}

func TestCurrentBranch(t *testing.T) {
	root := initTestRepo(t)
	branch, err := CurrentBranch(context.Background(), root)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch == "" {
		t.Fatal("CurrentBranch returned an empty branch in a fresh repo")
	}
	if _, err := CurrentBranch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("CurrentBranch must error outside a git repo")
	}
}
