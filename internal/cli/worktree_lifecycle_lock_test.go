package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestWorktreeLifecycleLockBlocksSameNameCreate(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	lock, err := lockWorktreeLifecycle(repoRoot, "locked-create")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := createManagedWorktree(repoRoot, "locked-create", "HEAD", "mivia/"); err == nil || !strings.Contains(err.Error(), "lock is busy") {
		t.Fatalf("create while lifecycle lock is held = %v, want busy lock", err)
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, "locked-create")
	if err != nil {
		t.Fatal(err)
	}
	if worktree != nil {
		t.Fatalf("blocked create produced worktree %+v", worktree)
	}
}

func TestWorktreeLifecycleLockBlocksRemovalBeforeDeletionFence(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := createManagedWorktree(repoRoot, "locked-remove", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockWorktreeLifecycle(repoRoot, worktree.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "lock is busy") {
		t.Fatalf("remove while lifecycle lock is held = %v, want busy lock", err)
	}
	assertManagedWorktreeActive(t, repoRoot, worktree)
}
