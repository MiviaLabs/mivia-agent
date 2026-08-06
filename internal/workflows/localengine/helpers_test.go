package localengine

import (
	"os/exec"
	"strings"
	"testing"
)

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestResolveLocalIdentityResolvesRealRepo pins that a git workspace gets its
// real default branch and HEAD commit. Fabricated identities ("main" /
// "local-base") made every delivery attempt refuse with "base commit is not
// an ancestor of HEAD".
func TestResolveLocalIdentityResolvesRealRepo(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	head := runGit(t, root, "rev-parse", "HEAD")

	baseRef, baseCommit, worktree, err := resolveLocalIdentity(root, "wfr-x")
	if err != nil {
		t.Fatal(err)
	}
	if baseCommit != head {
		t.Fatalf("baseCommit = %q, want %q", baseCommit, head)
	}
	if baseRef != "master" && baseRef != "main" {
		t.Fatalf("baseRef = %q, want the real default branch", baseRef)
	}
	if worktree != "workflow-wfr-x" {
		t.Fatalf("worktree = %q", worktree)
	}
}

// TestResolveLocalIdentityRejectsNonGitRoot pins that a non-git workspace is
// reported as an error instead of silently fabricating an identity.
func TestResolveLocalIdentityRejectsNonGitRoot(t *testing.T) {
	if _, _, _, err := resolveLocalIdentity(t.TempDir(), "wfr-x"); err == nil {
		t.Fatal("expected an error for a non-git root")
	}
}
