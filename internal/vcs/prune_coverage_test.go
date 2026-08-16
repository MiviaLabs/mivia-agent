package vcs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adminDirFor returns the git admin registration directory for a worktree
// name: the directory pruneStaleWorktree inspects and removes.
func adminDirFor(t *testing.T, root, name string) string {
	t.Helper()
	return filepath.Join(resolveGitDir(filepath.Join(root, ".git")), "worktrees", name)
}

// assertRegistrationIntact asserts that a worktree registration directory
// still exists on disk. Git's own worktree listing skips registrations whose
// pointer it cannot parse, so Resolve is the wrong probe here: the contract
// under test is that pruneStaleWorktree leaves the registration directory in
// place.
func assertRegistrationIntact(t *testing.T, admin string) {
	t.Helper()
	if _, err := os.Stat(admin); err != nil {
		t.Fatalf("registration directory %s was removed: %v", admin, err)
	}
}

// TestPruneInvalidNameErrors covers the SanitizeName error return: a reserved
// name is rejected before any registration work happens.
func TestPruneInvalidNameErrors(t *testing.T) {
	root := initTestRepo(t)
	if err := Prune(context.Background(), root, ".git"); err == nil {
		t.Fatal("Prune with a reserved name must error")
	}
}

// TestPrunePreservesLockedRegistration covers the locked guard: git's own
// contract is that a locked registration is never pruned, even when the
// working-tree directory is gone.
func TestPrunePreservesLockedRegistration(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "locked-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "locked-target")
	if err := os.WriteFile(filepath.Join(admin, "locked"), []byte("held by test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "locked-target"); err != nil {
		t.Fatalf("Prune locked registration: %v", err)
	}
	if resolved, err := Resolve(ctx, root, "locked-target"); err != nil {
		t.Fatal(err)
	} else if resolved == nil {
		t.Fatal("locked registration was pruned")
	}
}

// TestPrunePreservesUnparseablePointer covers the unparseable-pointer guard:
// a pointer that cannot name a working tree preserves the registration.
func TestPrunePreservesUnparseablePointer(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "garbage-pointer", "HEAD"); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "garbage-pointer")
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte("not a worktree pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "garbage-pointer"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	assertRegistrationIntact(t, admin)
}

// TestPrunePreservesUnstatableWorktree covers the fail-closed branch for a
// stat that fails for a reason other than IsNotExist: a pointer whose single
// path component exceeds NAME_MAX makes os.Stat return ENAMETOOLONG, and the
// registration must survive.
func TestPrunePreservesUnstatableWorktree(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "longstat", "HEAD"); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "longstat")
	long := strings.Repeat("a", 300)
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(long+"/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "longstat"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	assertRegistrationIntact(t, admin)
}

// TestPrunePreservesMissingPointerFile covers the unreadable-pointer guard:
// a missing gitdir pointer file preserves the registration.
func TestPrunePreservesMissingPointerFile(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "no-pointer", "HEAD"); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "no-pointer")
	if err := os.Remove(filepath.Join(admin, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "no-pointer"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	assertRegistrationIntact(t, admin)
}

// TestPrunePreservesUnreadablePointer covers the read-error guard: a pointer
// that opens but cannot be read (here: a directory in its place) preserves
// the registration.
func TestPrunePreservesUnreadablePointer(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "dir-pointer", "HEAD"); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "dir-pointer")
	pointer := filepath.Join(admin, "gitdir")
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pointer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "dir-pointer"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	assertRegistrationIntact(t, admin)
}

// TestPrunePreservesOversizedPointer covers the bounded-reader guard: a
// pointer larger than maxGitdirFileSize preserves the registration instead of
// exhausting memory.
func TestPrunePreservesOversizedPointer(t *testing.T) {
	root := initTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, root, "big-pointer", "HEAD"); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "big-pointer")
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(strings.Repeat("x", maxGitdirFileSize+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, root, "big-pointer"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	assertRegistrationIntact(t, admin)
}

// TestCreateWithPrefixLeaseSurfacesPruneFailure covers the prune error
// propagation in CreateWithPrefixLease: when the stale registration cannot be
// removed (unwritable registration directory), the failure must surface
// instead of proceeding into a broken `git worktree add`.
func TestCreateWithPrefixLeaseSurfacesPruneFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test does not apply to root")
	}
	root := initTestRepo(t)
	ctx := context.Background()
	wt, err := Create(ctx, root, "blocked-prune", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	admin := adminDirFor(t, root, "blocked-prune")
	if err := os.Chmod(admin, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(admin, 0o755) })
	if _, err := CreateWithPrefixLease(ctx, root, "blocked-prune", "HEAD", defaultWorktreeBranchPrefix, nil); err == nil {
		t.Fatal("CreateWithPrefixLease must surface the prune RemoveAll failure")
	}
	if resolved, err := Resolve(ctx, root, "blocked-prune"); err != nil {
		t.Fatal(err)
	} else if resolved == nil {
		t.Fatal("blocked prune must leave the registration intact")
	}
}
