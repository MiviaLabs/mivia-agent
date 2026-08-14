package delivery

// CountUnshippedCommits (spec-auto-split-oversized-prs.md §5.2-5.3): counts
// the commits left on a branch after the one delivery pushed, using a real
// git repo (RealGit), matching the repo's existing GitRunner test pattern
// (gitops_test.go's initRepo/runGit) rather than a fake git double.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountUnshippedCommits_NoneAfterDelivered(t *testing.T) {
	repo := initRepo(t)
	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	head := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))

	n, err := CountUnshippedCommits(context.Background(), RealGit{}, gc, head)
	if err != nil {
		t.Fatalf("CountUnshippedCommits: %v", err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0 (HEAD IS the delivered commit)", n)
	}
}

func TestCountUnshippedCommits_CountsTrailingCommits(t *testing.T) {
	repo := initRepo(t)
	deliveredHead := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))

	// Two trailing commits on top of the delivered one, as a diff-size
	// repair's commit stack would leave (§5.2): the review-sized slice
	// (already delivered as deliveredHead) plus deferred-scope commits.
	writeAndCommit(t, repo, "a.txt", "a", "deferred: chunk 2")
	writeAndCommit(t, repo, "b.txt", "b", "deferred: chunk 3")

	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	n, err := CountUnshippedCommits(context.Background(), RealGit{}, gc, deliveredHead)
	if err != nil {
		t.Fatalf("CountUnshippedCommits: %v", err)
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2 trailing commits", n)
	}
}

func TestCountUnshippedCommits_RejectsEmptyCommit(t *testing.T) {
	repo := initRepo(t)
	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	if _, err := CountUnshippedCommits(context.Background(), RealGit{}, gc, ""); err == nil {
		t.Fatal("expected an error for an empty delivered commit")
	}
	if _, err := CountUnshippedCommits(context.Background(), RealGit{}, gc, "   "); err == nil {
		t.Fatal("expected an error for a blank delivered commit")
	}
}

func TestCountUnshippedCommits_RejectsUnknownCommit(t *testing.T) {
	repo := initRepo(t)
	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	if _, err := CountUnshippedCommits(context.Background(), RealGit{}, gc, "0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("expected an error for a commit git cannot resolve")
	}
}

// writeAndCommit writes content to a file in repo and commits it, for
// building a linear history of "trailing" commits after a delivered head.
func writeAndCommit(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", message, "--quiet")
}
