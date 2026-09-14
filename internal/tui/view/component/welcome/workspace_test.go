package welcome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseHEAD_Ref(t *testing.T) {
	branch, ok := parseHEAD([]byte("ref: refs/heads/main\n"))
	if !ok {
		t.Fatalf("parseHEAD(ref) ok = false, want true")
	}
	if branch != "main" {
		t.Errorf("parseHEAD(ref) branch = %q, want %q", branch, "main")
	}
}

func TestParseHEAD_Detached(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	branch, ok := parseHEAD([]byte(sha + "\n"))
	if !ok {
		t.Fatalf("parseHEAD(detached) ok = false, want true")
	}
	if branch != sha[:7] {
		t.Errorf("parseHEAD(detached) branch = %q, want %q", branch, sha[:7])
	}
}

func TestParseHEAD_Empty(t *testing.T) {
	cases := [][]byte{
		[]byte(""),
		[]byte("\n"),
		[]byte("garbage that is not a ref or a sha\n"),
		[]byte("ref: refs/heads/\n"),
	}
	for _, data := range cases {
		branch, ok := parseHEAD(data)
		if ok {
			t.Errorf("parseHEAD(%q) ok = true, want false", data)
		}
		if branch != "" {
			t.Errorf("parseHEAD(%q) branch = %q, want empty", data, branch)
		}
	}
}

// writeRepo creates a minimal plain-form .git directory (dir form, no
// gitdir indirection) at repoDir/.git with the given HEAD contents.
func writeRepo(t *testing.T, repoDir, headContents string) {
	t.Helper()
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(headContents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectWorkspace_PlainRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepo(t, repo, "ref: refs/heads/feature-x\n")

	repoName, branch, ok := detectWorkspace(repo)
	if !ok {
		t.Fatalf("detectWorkspace(plain repo) ok = false, want true")
	}
	if repoName != filepath.Base(repo) {
		t.Errorf("repoName = %q, want %q", repoName, filepath.Base(repo))
	}
	if branch != "feature-x" {
		t.Errorf("branch = %q, want %q", branch, "feature-x")
	}
}

func TestDetectWorkspace_LinkedWorktree(t *testing.T) {
	root := t.TempDir()

	// The "main" repo, with its own distinct HEAD/branch.
	mainRepo := filepath.Join(root, "main-repo")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepo(t, mainRepo, "ref: refs/heads/main\n")

	// A separate git-common-dir representing the linked worktree's real
	// git directory, with a distinct HEAD/branch to prove indirection.
	worktreeGitDir := filepath.Join(root, "worktrees-admin", "feature-y")
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGitDir, "HEAD"), []byte("ref: refs/heads/feature-y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The linked worktree's working directory: .git is a FILE pointing at
	// worktreeGitDir, not the main repo's .git.
	worktreeDir := filepath.Join(root, "worktree-checkout")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoName, branch, ok := detectWorkspace(worktreeDir)
	if !ok {
		t.Fatalf("detectWorkspace(linked worktree) ok = false, want true")
	}
	if repoName != filepath.Base(worktreeDir) {
		t.Errorf("repoName = %q, want %q", repoName, filepath.Base(worktreeDir))
	}
	if branch != "feature-y" {
		t.Errorf("branch = %q, want %q (must follow gitdir indirection, not read main repo HEAD)", branch, "feature-y")
	}
}

func TestDetectWorkspace_WalksUpFromSubdir(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo-root")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepo(t, repo, "ref: refs/heads/dev\n")

	sub := filepath.Join(repo, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	rootName, rootBranch, rootOK := detectWorkspace(repo)
	subName, subBranch, subOK := detectWorkspace(sub)

	if !rootOK || !subOK {
		t.Fatalf("detectWorkspace ok mismatch: root=%v sub=%v, want both true", rootOK, subOK)
	}
	if rootName != subName || rootBranch != subBranch {
		t.Errorf("walk-up result (%q, %q) != root result (%q, %q)", subName, subBranch, rootName, rootBranch)
	}
}

func TestDetectWorkspace_NoRepo(t *testing.T) {
	// Isolated tree with no .git anywhere; use a deep subdir so a broken
	// walk-up would loop rather than terminate quickly.
	dir := filepath.Join(t.TempDir(), "x", "y", "z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoName, branch, ok := detectWorkspace(dir)
	if ok {
		t.Fatalf("detectWorkspace(no repo) ok = true, want false")
	}
	if repoName != "" || branch != "" {
		t.Errorf("detectWorkspace(no repo) = (%q, %q), want empty", repoName, branch)
	}
}
