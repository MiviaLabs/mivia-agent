package vcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveGitDirRelativePointer is the regression test for the DC-10
// defect: resolveGitDir anchored a relative gitdir pointer to the process
// working directory instead of the directory that contains the .git file.
// Git's own read_gitfile_gently anchors a relative pointer to the gitfile's
// directory (git setup.c). This test fails on the old code and passes after
// the fix.
func TestResolveGitDirRelativePointer(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	actualGit := filepath.Join(dir, "actual-git")
	if err := os.MkdirAll(actualGit, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(repo, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: ../actual-git\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Move the working directory away from the gitfile's directory so the
	// old CWD-anchored resolution differs from the gitfile-anchored one.
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	got := resolveGitDir(gitFile)
	want, _ := filepath.Abs(filepath.Join(filepath.Dir(gitFile), "../actual-git"))
	if got != want {
		t.Errorf("resolveGitDir(relative pointer) = %q, want %q", got, want)
	}
}

// TestGitdirPointer pins the pure gitdirPointer contract table-driven:
// trim-then-prefix, first line wins, ok is true only when the prefix is
// present and the pointer is non-empty, and no size cap (git's 1 MiB cap is
// a separate, known candidate left for a later run).
func TestGitdirPointer(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantPtr string
		wantOK  bool
	}{
		{"valid relative", "gitdir: ../actual-git\n", "../actual-git", true},
		{"valid absolute", "gitdir: /abs/git\n", "/abs/git", true},
		{"crlf", "gitdir: ../x\r\n", "../x", true},
		{"trailing whitespace", "gitdir: ../x   \n", "../x", true},
		{"leading whitespace", "  gitdir: ../x\n", "../x", true},
		{"empty", "", "", false},
		{"whitespace only", "   \n", "", false},
		{"malformed no space", "gitdir:/abs\n", "", false},
		{"empty pointer", "gitdir: \n", "", false},
		{"non gitdir body", "not a gitdir line\n", "", false},
		{"duplicate lines", "gitdir: first\ngitdir: second\n", "first", true},
		{"oversized", "gitdir: ../actual-git\n" + strings.Repeat("a", 2<<20), "../actual-git", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ptr, ok := gitdirPointer([]byte(tc.input))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ptr != tc.wantPtr {
				t.Errorf("pointer = %q, want %q", ptr, tc.wantPtr)
			}
		})
	}
}

// TestResolveGitDirAbsoluteAndNonGitdir covers the resolveGitDir branches
// the existing tests leave open: an absolute pointer is returned unchanged,
// a non-gitdir body returns the input path, and a pointer with .. segments
// anchors to the gitfile directory (the same path git itself resolves), so
// the fix adds no new escape. None of the branches may panic.
func TestResolveGitDirAbsoluteAndNonGitdir(t *testing.T) {
	dir := t.TempDir()

	// Absolute pointer: unchanged.
	absGit := filepath.Join(dir, "abs-git")
	if err := os.MkdirAll(absGit, 0o755); err != nil {
		t.Fatal(err)
	}
	absFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(absFile, []byte("gitdir: "+absGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveGitDir(absFile); got != absGit {
		t.Errorf("resolveGitDir(absolute pointer) = %q, want %q", got, absGit)
	}

	// Non-gitdir body: input path unchanged.
	bodyFile := filepath.Join(dir, ".git-not-a-pointer")
	if err := os.WriteFile(bodyFile, []byte("not a gitdir line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	absBody, _ := filepath.Abs(bodyFile)
	if got := resolveGitDir(bodyFile); got != absBody {
		t.Errorf("resolveGitDir(non-gitdir body) = %q, want %q", got, absBody)
	}

	// Pointer with .. segments: anchored to the gitfile directory.
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "other", "git")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(repo, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: ../other/git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got := resolveGitDir(gitFile)
	want, _ := filepath.Abs(filepath.Join(filepath.Dir(gitFile), "../other/git"))
	if got != want {
		t.Errorf("resolveGitDir(.. pointer) = %q, want %q", got, want)
	}
}
