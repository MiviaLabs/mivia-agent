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
	if want := "mivia/feature-auth"; wt.Branch != want {
		t.Errorf("Branch = %q, want %q", wt.Branch, want)
	}
	// Verify the path exists.
	if _, err := os.Stat(wt.Path); err != nil {
		t.Errorf("worktree path does not exist: %v", err)
	}
}

func TestIntegrationCreateWithPrefix(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := CreateWithPrefix(ctx, root, "custom-prefix", "HEAD", "agent/")
	if err != nil {
		t.Fatalf("CreateWithPrefix: %v", err)
	}
	if want := "agent/custom-prefix"; wt.Branch != want {
		t.Errorf("Branch = %q, want %q", wt.Branch, want)
	}

	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/agent/custom-prefix")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatalf("custom managed branch is not created: %v", err)
	}
}

// TestIntegrationRemovePreservesManagedBranchAfterBranchChange verifies that
// removal does not delete the configured managed branch when the worktree
// changes to another branch before removal.
func TestIntegrationRemovePreservesManagedBranchAfterBranchChange(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := CreateWithPrefix(ctx, root, "preserve-branch", "HEAD", "agent/")
	if err != nil {
		t.Fatalf("CreateWithPrefix: %v", err)
	}
	run(t, wt.Path, "git", "switch", "-c", "feature/alternate")
	if err := RemoveWithPrefix(ctx, root, wt.Name, "agent/"); err != nil {
		t.Fatalf("RemoveWithPrefix: %v", err)
	}

	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+wt.Branch)
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatalf("managed branch %q is removed after the worktree changes branch: %v", wt.Branch, err)
	}
}

// TestIntegrationCreateWithPrefixReusesRetainedBranch verifies that a
// configured managed branch remains available after removal. A later create
// with the same name must check out the retained branch without reset.
func TestIntegrationCreateWithPrefixReusesRetainedBranch(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	const name = "reuse-branch"
	const prefix = "agent/"

	worktree, err := CreateWithPrefix(ctx, root, name, "HEAD", prefix)
	if err != nil {
		t.Fatalf("first CreateWithPrefix: %v", err)
	}
	write(t, worktree.Path, "retained.txt", "retain this commit")
	run(t, worktree.Path, "git", "add", "retained.txt")
	run(t, worktree.Path, "git", "commit", "-m", "retain worktree branch")

	retainedHeadCmd := exec.Command("git", "rev-parse", "HEAD")
	retainedHeadCmd.Dir = worktree.Path
	retainedHeadOut, err := retainedHeadCmd.Output()
	if err != nil {
		t.Fatalf("get retained branch HEAD: %v", err)
	}
	retainedHead := strings.TrimSpace(string(retainedHeadOut))

	if err := RemoveWithPrefix(ctx, root, name, prefix); err != nil {
		t.Fatalf("RemoveWithPrefix: %v", err)
	}

	recreated, err := CreateWithPrefix(ctx, root, name, "HEAD", prefix)
	if err != nil {
		t.Fatalf("second CreateWithPrefix: %v", err)
	}
	if want := prefix + name; recreated.Branch != want {
		t.Errorf("Branch = %q, want %q", recreated.Branch, want)
	}

	recreatedHeadCmd := exec.Command("git", "rev-parse", "HEAD")
	recreatedHeadCmd.Dir = recreated.Path
	recreatedHeadOut, err := recreatedHeadCmd.Output()
	if err != nil {
		t.Fatalf("get recreated branch HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(recreatedHeadOut)); got != retainedHead {
		t.Errorf("recreated branch HEAD = %q, want retained HEAD %q", got, retainedHead)
	}
	if _, err := os.Stat(filepath.Join(recreated.Path, "retained.txt")); err != nil {
		t.Errorf("retained branch content is missing: %v", err)
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

func TestCreateRejectsTruncatedName(t *testing.T) {
	root := initTestRepo(t)
	name := strings.Repeat("a", MaxWorktreeNameLen+1)
	if _, err := Create(context.Background(), root, name, "HEAD"); err == nil {
		t.Fatal("create with a truncated name succeeds")
	}
	worktrees, err := List(context.Background(), root)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("worktree count = %d, want 0", len(worktrees))
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

func TestRemoveRejectsTruncatedName(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	name := strings.Repeat("a", MaxWorktreeNameLen)
	worktree, err := Create(ctx, root, name, "HEAD")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := Remove(ctx, root, name+"suffix"); err == nil {
		t.Fatal("remove with a truncated name succeeds")
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatalf("worktree is removed after rejected name: %v", err)
	}
}

func TestRemove_PreservesBranch(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	name := "wt-1"
	wt, err := Create(ctx, root, name, "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	branchName := "mivia/" + name

	// Verify the branch was created.
	branchCmd := exec.Command("git", "branch", "--list", branchName)
	branchCmd.Dir = root
	out, err := branchCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatalf("branch %q should exist after Create, but was not found", branchName)
	}

	// Remove the worktree.
	if err := Remove(ctx, root, name); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify the worktree directory is gone.
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists after Remove")
	}

	// Verify the branch remains after Remove.
	branchCmd = exec.Command("git", "branch", "--list", branchName)
	branchCmd.Dir = root
	out, err = branchCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list after remove: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Errorf("branch %q is removed after Remove", branchName)
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

func TestMainRepoRoot_FromMainTree(t *testing.T) {
	root := initTestRepo(t)
	got, err := MainRepoRoot(root)
	if err != nil {
		t.Fatalf("MainRepoRoot: %v", err)
	}
	abs, _ := filepath.Abs(root)
	if got != abs {
		t.Errorf("MainRepoRoot from main tree = %q, want %q", got, abs)
	}
}

func TestMainRepoRoot_FromWorktree(t *testing.T) {
	root := initTestRepo(t)
	// Create a linked worktree (not mivia-managed, just a plain git worktree).
	wtPath := filepath.Join(root, "linked-wt")
	run(t, root, "git", "worktree", "add", wtPath, "-b", "wt/test-main-root", "HEAD")

	got, err := MainRepoRoot(wtPath)
	if err != nil {
		t.Fatalf("MainRepoRoot from worktree: %v", err)
	}
	abs, _ := filepath.Abs(root)
	if got != abs {
		t.Errorf("MainRepoRoot from worktree = %q, want main root %q", got, abs)
	}
}

func TestMainRepoRoot_FromMiviaWorktree(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "test-main-root-wt", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := MainRepoRoot(wt.Path)
	if err != nil {
		t.Fatalf("MainRepoRoot from mivia worktree: %v", err)
	}
	abs, _ := filepath.Abs(root)
	if got != abs {
		t.Errorf("MainRepoRoot from mivia worktree = %q, want main root %q", got, abs)
	}
}

func TestMainRepoRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	_, err := MainRepoRoot(dir)
	if _, ok := err.(NotGitRepoError); !ok {
		t.Errorf("expected NotGitRepoError, got %T: %v", err, err)
	}
}

// TestNoShellPatterns ensures vcs/ never uses shell execution for git.
func TestNoShellPatterns(t *testing.T) {
	root := repoRootForVCS(t)
	// Match shell patterns: exec.CommandContext with "sh", "bash", or shell metacharacters.
	shell := regexp.MustCompile(`exec\.Command[^C].*"sh"|exec\.Command[^C].*"bash"|exec\.Command\("git"[^)]*-c[ "]`)
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

func TestIsWorktree_MainTree(t *testing.T) {
	root := initTestRepo(t)
	// Main tree should not report as a worktree.
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if IsWorktree() {
		t.Error("IsWorktree() = true in main tree, want false")
	}
}

func TestIsWorktree_LinkedWorktree(t *testing.T) {
	root := initTestRepo(t)
	// Create a bare git worktree (not managed by mivia).
	wtPath := filepath.Join(root, "bare-worktree")
	run(t, root, "git", "worktree", "add", wtPath, "-b", "wt/test-bare", "HEAD")

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(wtPath); err != nil {
		t.Fatal(err)
	}
	if !IsWorktree() {
		t.Error("IsWorktree() = false in linked worktree, want true")
	}
	// Verify it still returns false when we go back to the main tree.
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if IsWorktree() {
		t.Error("IsWorktree() = true back in main tree, want false")
	}
}

func TestIsWorktree_NotARepo(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if IsWorktree() {
		t.Error("IsWorktree() = true outside any repo, want false")
	}
}

func TestDetectWorktreeName_MainTree(t *testing.T) {
	root := initTestRepo(t)
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if got := DetectWorktreeName(); got != "" {
		t.Errorf("DetectWorktreeName() = %q in main tree, want empty", got)
	}
}

func TestDetectWorktreeName_MiviaWorktree(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "feature-login", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(wt.Path); err != nil {
		t.Fatal(err)
	}
	if got := DetectWorktreeName(); got != "feature-login" {
		t.Errorf("DetectWorktreeName() = %q, want %q", got, "feature-login")
	}
}

func TestDetectWorktreeName_BareWorktree(t *testing.T) {
	root := initTestRepo(t)
	// Create a bare git worktree (not managed by mivia).
	wtPath := filepath.Join(root, "bare-test-wt")
	run(t, root, "git", "worktree", "add", wtPath, "-b", "wt/test-bare-detect", "HEAD")

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(wtPath); err != nil {
		t.Fatal(err)
	}
	if got := DetectWorktreeName(); got != "bare-test-wt" {
		t.Errorf("DetectWorktreeName() = %q, want %q", got, "bare-test-wt")
	}
}

func TestDetectWorktreeName_NotARepo(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := DetectWorktreeName(); got != "" {
		t.Errorf("DetectWorktreeName() = %q outside any repo, want empty", got)
	}
}

func TestDetectBranch(t *testing.T) {
	root := initTestRepo(t)
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	got := DetectBranch()
	if got == "" {
		t.Error("DetectBranch() = empty, want branch name")
	}
}

func TestDetectBranch_NotARepo(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := DetectBranch(); got != "" {
		t.Errorf("DetectBranch() = %q outside any repo, want empty", got)
	}
}

func TestResolveGitDir_Directory(t *testing.T) {
	root := initTestRepo(t)
	gitDir := filepath.Join(root, ".git")
	got := resolveGitDir(gitDir)
	if got != gitDir {
		t.Errorf("resolveGitDir(.git dir) = %q, want %q", got, gitDir)
	}
}

func TestResolveGitDir_GitdirFile(t *testing.T) {
	dir := t.TempDir()
	// Create a .git file with a gitdir: pointer.
	targetDir := filepath.Join(dir, "actual-git")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(dir, ".git")
	content := "gitdir: " + targetDir + "\n"
	if err := os.WriteFile(gitFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveGitDir(gitFile)
	absTarget, _ := filepath.Abs(targetDir)
	if got != absTarget {
		t.Errorf("resolveGitDir(.git file) = %q, want %q", got, absTarget)
	}
}

func TestResolveGitDir_Nonexistent(t *testing.T) {
	got := resolveGitDir("/nonexistent/path/.git")
	if got != "/nonexistent/path/.git" {
		t.Errorf("resolveGitDir(nonexistent) = %q, want /nonexistent/path/.git", got)
	}
}

func TestCurrentWorktreeNameFromWorktreeRoot(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "wt-name", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	name, err := CurrentWorktreeName(ctx, wt.Path)
	if err != nil {
		t.Fatalf("CurrentWorktreeName: %v", err)
	}
	if name != "wt-name" {
		t.Errorf("name = %q, want wt-name", name)
	}
}

// TestCurrentWorktreeNameFromWorktreeSubdir pins the fix where RepoRoot alone
// returned the worktree's own toplevel, so the .mivia/worktrees prefix check
// could never match from inside a worktree, and subdirectories of a worktree
// must still resolve to the worktree name.
func TestCurrentWorktreeNameFromWorktreeSubdir(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "wt-name", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sub := filepath.Join(wt.Path, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := CurrentWorktreeName(ctx, sub)
	if err != nil {
		t.Fatalf("CurrentWorktreeName: %v", err)
	}
	if name != "wt-name" {
		t.Errorf("name from subdir = %q, want wt-name", name)
	}
}

func TestCurrentWorktreeNameFromMainTree(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	name, err := CurrentWorktreeName(ctx, root)
	if err != nil {
		t.Fatalf("CurrentWorktreeName: %v", err)
	}
	if name != "" {
		t.Errorf("name = %q, want empty in the main tree", name)
	}
}

// TestPruneRemovesStaleWorktreeEntry pins the fix where a worktree whose
// directory is gone stays listed by git until `git worktree prune` runs.
// The orphan removal path relies on Prune to clear the stale entry after
// RemoveWithPrefixLease reports the directory as already gone.
func TestPruneRemovesStaleWorktreeEntry(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "prune-target", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatalf("RemoveAll worktree directory: %v", err)
	}
	resolved, err := Resolve(ctx, root, "prune-target")
	if err != nil {
		t.Fatalf("Resolve before prune: %v", err)
	}
	if resolved == nil {
		t.Fatal("git still lists a worktree whose directory is gone")
	}
	if err := Prune(ctx, root); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	resolved, err = Resolve(ctx, root, "prune-target")
	if err != nil {
		t.Fatalf("Resolve after prune: %v", err)
	}
	if resolved != nil {
		t.Fatalf("Resolve after prune = %+v, want nil", resolved)
	}
}

// TestCreateReusesNameAfterStaleRegistration pins the fix where a worktree
// whose checkout was removed outside Remove (orphan cleanup) stays registered
// in .git/worktrees/<name>/ until `git worktree prune` runs, and the stale
// registration makes `git worktree add` fail for the same name, wedging the
// name permanently. Create now prunes stale registrations first, so the add
// re-creates the worktree and re-attaches the retained branch.
func TestCreateReusesNameAfterStaleRegistration(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	const name = "stale-reuse"

	worktree, err := Create(ctx, root, name, "HEAD")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	write(t, worktree.Path, "retained.txt", "retain this commit")
	run(t, worktree.Path, "git", "add", "retained.txt")
	run(t, worktree.Path, "git", "commit", "-m", "retain worktree branch")

	if err := os.RemoveAll(worktree.Path); err != nil {
		t.Fatalf("RemoveAll worktree directory: %v", err)
	}
	// Wedge precondition: git still lists the stale registration, matching
	// the state TestPruneRemovesStaleWorktreeEntry constructs.
	resolved, err := Resolve(ctx, root, name)
	if err != nil {
		t.Fatalf("Resolve before re-create: %v", err)
	}
	if resolved == nil {
		t.Fatal("git no longer lists the stale worktree; wedge precondition missing")
	}

	recreated, err := Create(ctx, root, name, "HEAD")
	if err != nil {
		t.Fatalf("re-Create with stale registration: %v", err)
	}
	if recreated.Name != name {
		t.Errorf("Name = %q, want %q", recreated.Name, name)
	}
	if want := defaultWorktreeBranchPrefix + name; recreated.Branch != want {
		t.Errorf("Branch = %q, want %q", recreated.Branch, want)
	}
	if _, err := os.Stat(filepath.Join(recreated.Path, "retained.txt")); err != nil {
		t.Errorf("retained branch content is missing after re-create: %v", err)
	}

	// Negative path: a second create of the now-live name is refused.
	if _, err := Create(ctx, root, name, "HEAD"); err == nil {
		t.Fatal("second Create of the live name succeeds")
	} else if _, ok := err.(WorktreeExistsError); !ok {
		t.Errorf("second Create error = %T: %v, want WorktreeExistsError", err, err)
	}
}
