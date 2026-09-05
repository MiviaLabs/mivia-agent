package vcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lock's two shape guards are each a disjunction:
//
//	info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()   (lock file)
//	info.Mode()&os.ModeSymlink != 0 || !info.IsDir()              (lock dir)
//
// The existing tests exercise only the SECOND arm of each (a fifo, a symlinked
// directory), so a mutant that turns either "||" into "&&" survives: the arm
// those tests trip is no longer sufficient on its own. These cover the first
// arm and the success path, which together pin both arms and the negation.

// TestLifecycleLockRejectsSymlinkedLockFile covers the symlink arm of the lock
// FILE guard. A symlinked lock is the dangerous shape - it redirects the flock
// to a file outside the repository's own git directory.
func TestLifecycleLockRejectsSymlinkedLockFile(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "outside.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if err := os.Symlink(target, filepath.Join(base, lockPath)); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	file, unlock, err := openWorktreeLifecycleLockFile(root, lockPath)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if err == nil {
		t.Fatal("a symlinked lifecycle lock file was accepted; it redirects the lock outside the git directory")
	}
}

// TestLifecycleLockRejectsRegularFileAsLockDir covers the IsDir arm of the
// lock DIRECTORY guard with a plain file - not a symlink - so it is the
// complement of TestLifecycleLockRejectsSymlinkLockDirectory.
func TestLifecycleLockRejectsRegularFileAsLockDir(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, worktreeLifecycleLockDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = ensureRegularLifecycleLockDir(root)
	if err == nil {
		t.Fatal("a regular file standing where the lock directory belongs was accepted")
	}
	if !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("error = %v, want it to name the directory shape", err)
	}
}

// TestLifecycleLockAcceptsAFreshRegularLock is the success path, and it is
// what makes the guards' NEGATIONS load-bearing: with `!IsRegular()` dropped,
// or with the not-exist tolerance inverted, a perfectly ordinary first lock
// would be refused and every worktree operation would stop working.
func TestLifecycleLockAcceptsAFreshRegularLock(t *testing.T) {
	repo := initTestRepo(t)

	lock, err := LockWorktreeLifecycle(repo, "wt-fresh")
	if err != nil {
		t.Fatalf("LockWorktreeLifecycle on a fresh repository = %v, want it to succeed", err)
	}
	if lock == nil || lock.File() == nil {
		t.Fatal("LockWorktreeLifecycle returned no usable lock")
	}
	lock.Close()

	// Taking it again after release must also succeed: the lock file now
	// EXISTS and is regular, which is the other side of the same guard.
	again, err := LockWorktreeLifecycle(repo, "wt-fresh")
	if err != nil {
		t.Fatalf("re-locking an existing regular lock file = %v, want it to succeed", err)
	}
	again.Close()
}
