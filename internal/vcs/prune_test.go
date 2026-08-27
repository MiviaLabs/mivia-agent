package vcs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPruneRemovesStaleWorktreeEntry pins the fix where a worktree whose
// directory is gone stays listed by git until its registration is cleared.
// The orphan removal path relies on Prune to clear the stale entry after
// RemoveWithPrefixLease reports the directory as already gone. The targeted
// prune removes exactly this one registration (its recorded working-tree
// directory is confirmed gone) and nothing else.
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
	if err := Prune(ctx, root, "prune-target"); err != nil {
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

// TestPruneKeepsIntactWorktreeWithBrokenGitfile is the regression test for
// VCS-1: `git worktree prune` drops a registration whenever the worktree's
// on-disk .git gitfile is missing, even when the working tree directory is
// intact (should_prune_worktree's admin index-mtime fallback is defeated at
// prune's expire=TIME_MAX). A live mivia worktree must stay discoverable via
// List/Resolve. Red before the fix: deleting keep-me's .git gitfile and then
// creating another worktree ran the old repo-wide prune, which dropped the
// keep-me registration and Resolve returned nil.
func TestPruneKeepsIntactWorktreeWithBrokenGitfile(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()

	keep, err := Create(ctx, root, "keep-me", "HEAD")
	if err != nil {
		t.Fatalf("Create keep-me: %v", err)
	}
	// Break the on-disk .git gitfile but keep the working tree directory.
	if err := os.Remove(filepath.Join(keep.Path, ".git")); err != nil {
		t.Fatalf("Remove keep-me .git gitfile: %v", err)
	}
	resolved, err := Resolve(ctx, root, "keep-me")
	if err != nil {
		t.Fatalf("Resolve keep-me before other create: %v", err)
	}
	if resolved == nil {
		t.Fatal("keep-me registration vanished before any prune ran")
	}

	// The same flow the old code wedged: creating another worktree pruned
	// repo-wide and silently dropped the intact keep-me registration.
	if _, err := Create(ctx, root, "other", "HEAD"); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	resolved, err = Resolve(ctx, root, "keep-me")
	if err != nil {
		t.Fatalf("Resolve keep-me after other create: %v", err)
	}
	if resolved == nil {
		t.Fatal("live worktree keep-me was pruned even though its working tree is intact")
	}
	// And the intact working tree still exists on disk.
	if _, err := os.Stat(keep.Path); err != nil {
		t.Fatalf("keep-me working tree directory is gone: %v", err)
	}
}

// TestPruneRefusesIntactWorktreeWithBrokenGitfile is the negative path of the
// same contract: calling Prune directly on an intact worktree whose .git
// gitfile is broken must not drop its registration or its working tree.
func TestPruneRefusesIntactWorktreeWithBrokenGitfile(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()

	wt, err := Create(ctx, root, "intact-broken", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Remove(filepath.Join(wt.Path, ".git")); err != nil {
		t.Fatalf("Remove .git gitfile: %v", err)
	}
	if err := Prune(ctx, root, "intact-broken"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	resolved, err := Resolve(ctx, root, "intact-broken")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved == nil {
		t.Fatal("Prune dropped an intact worktree whose .git gitfile is broken")
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("intact working tree directory is gone: %v", err)
	}
	// The registration directory itself must still be present.
	adminDir := filepath.Join(resolveGitDir(filepath.Join(root, ".git")), "worktrees", "intact-broken")
	if _, err := os.Stat(filepath.Join(adminDir, "gitdir")); err != nil {
		t.Fatalf("registration was removed: %v", err)
	}
}

// rootedTestPath builds a synthetic absolute path anchored at a root that
// filepath.IsAbs accepts on this platform. On Windows "\repo\..." is NOT
// filepath.IsAbs (no volume letter), so synthetic roots carry the real temp
// volume ("D:\repo\..."); on Unix the plain separator root is unchanged.
func rootedTestPath(elements ...string) string {
	root := string(filepath.Separator)
	if vol := filepath.VolumeName(os.TempDir()); vol != "" {
		root = vol
	}
	return filepath.Join(append([]string{root}, elements...)...)
}

// TestWorktreePathFromGitdir pins the pure pointer parser used by the
// targeted prune on edge inputs: empty, malformed, CRLF, duplicate lines,
// absolute and relative pointers, and a non-.git tail all parse without
// panicking and yield either a non-empty path or "".
func TestWorktreePathFromGitdir(t *testing.T) {
	adminDir := rootedTestPath("repo", ".git", "worktrees", "wt-a")
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace only", "  \r\n", ""},
		{"absolute with newline", rootedTestPath("repo", ".mivia", "worktrees", "wt-a", ".git") + "\n", rootedTestPath("repo", ".mivia", "worktrees", "wt-a")},
		{"absolute without newline", rootedTestPath("repo", ".mivia", "worktrees", "wt-a", ".git"), rootedTestPath("repo", ".mivia", "worktrees", "wt-a")},
		{"crlf", rootedTestPath("repo", ".mivia", "worktrees", "wt-a", ".git") + "\r\n", rootedTestPath("repo", ".mivia", "worktrees", "wt-a")},
		{"duplicate lines first wins", rootedTestPath("repo", ".mivia", "worktrees", "wt-a", ".git") + "\n" + rootedTestPath("repo", ".mivia", "worktrees", "wt-b", ".git") + "\n", rootedTestPath("repo", ".mivia", "worktrees", "wt-a")},
		{"gitdir prefix is not a pointer", "gitdir: " + rootedTestPath("elsewhere", ".git") + "\n", ""},
		{"non gitdir tail", rootedTestPath("repo", "not-a-gitfile") + "\n", ""},
		{"malformed relative", "..\n", ""},
		{"blank first line", "\n" + rootedTestPath("repo", ".mivia", "worktrees", "wt-a", ".git") + "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := worktreePathFromGitdir([]byte(tc.input), adminDir)
			if got != tc.want {
				t.Errorf("worktreePathFromGitdir(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	// Relative pointers anchor to the admin worktree directory, matching
	// git's own anchoring (get_linked_worktree and should_prune_worktree).
	relAdmin := rootedTestPath("repo", ".git", "worktrees", "wt-a")
	got := worktreePathFromGitdir([]byte("../.mivia/worktrees/wt-a/.git\n"), relAdmin)
	want := filepath.Clean(filepath.Join(relAdmin, "../.mivia/worktrees/wt-a"))
	if got != want {
		t.Errorf("worktreePathFromGitdir(relative) = %q, want %q", got, want)
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
