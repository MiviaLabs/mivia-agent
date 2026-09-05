//go:build unix

package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLifecycleLockRejectsFinalSymlinkRace(t *testing.T) {
	repo := initTestRepo(t)
	original := lstatLifecyclePath
	lstatLifecyclePath = func(root *os.Root, path string) (os.FileInfo, error) {
		if path != filepath.Join(worktreeLifecycleLockDir, "victim.lock") {
			return original(root, path)
		}
		dir := filepath.Join(root.Name(), worktreeLifecycleLockDir)
		if err := os.WriteFile(filepath.Join(dir, "target.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target.lock", filepath.Join(dir, "victim.lock")); err != nil {
			t.Fatal(err)
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { lstatLifecyclePath = original })

	lock, err := LockWorktreeLifecycle(repo, "victim")
	if lock != nil {
		lock.Close()
	}
	if err == nil {
		t.Fatal("lifecycle lock succeeded through a final symlink")
	}
}

func TestLifecycleExactUnixFileErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openWorktreeLifecycleLockFile(root, "lock"); err == nil {
		t.Fatal("closed root opened a lifecycle lock")
	}
	file, err := os.CreateTemp(t.TempDir(), "marker-lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LockWorktreeMarkerFile(file); err == nil {
		t.Fatal("closed marker descriptor acquired a lock")
	}
}

func TestLifecycleExactClosedRootWithValidLockPath(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("closed root opened a lifecycle lock")
	}
}

func TestLifecycleLockRejectsMovedCommonDirectory(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "common")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(original, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("lifecycle lock opened in a moved-away common directory")
	}
}

func TestLifecycleLockRejectsReplacedCommonDirectory(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "common")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(original, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(original, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	file, unlock, err := openWorktreeLifecycleLockFile(root, path)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("replaced common directory error = %v, want identity failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(original, worktreeLifecycleLockDir, "victim.lock")); statErr == nil {
		t.Fatal("lifecycle lock leaked into the replacement common directory")
	}
}

func TestLifecycleLockRejectsSymlinkLockDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, worktreeLifecycleLockDir)); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("lifecycle lock opened through a symlinked lock directory")
	}
}

func TestLifecycleFaultSeamLockDirectoryFirstUseRace(t *testing.T) {
	repo := initTestRepo(t)
	commonDir, err := WorktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
	})
	realDir := t.TempDir()
	dirInfo, err := os.Lstat(realDir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return dirInfo, nil
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularLifecycleLockDir(root); err != nil {
		t.Fatalf("concurrent first-use lock directory creation failed: %v", err)
	}
}

func TestLifecycleFaultSeamLockDirectoryFirstUseRaceKeepsFailClosed(t *testing.T) {
	repo := initTestRepo(t)
	commonDir, err := WorktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
	})
	planted := filepath.Join(t.TempDir(), "planted")
	if err := os.Symlink("elsewhere", planted); err != nil {
		t.Fatal(err)
	}
	symlinkInfo, err := os.Lstat(planted)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return symlinkInfo, nil
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularLifecycleLockDir(root); err == nil {
		t.Fatal("concurrent symlink planting at the lock directory was accepted")
	}
}

func TestLifecycleFaultSeamLockFileStatError(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("lock file stat fault")
	original := statLifecycleLockFile
	statLifecycleLockFile = func(*os.File) (os.FileInfo, error) { return nil, sentinel }
	t.Cleanup(func() { statLifecycleLockFile = original })
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	file, unlock, err := openWorktreeLifecycleLockFile(root, path)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("lock file stat fault error = %v", err)
	}
}

func TestLifecycleLockRejectsFifoLockFile(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lockPath := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if err := syscall.Mkfifo(filepath.Join(base, lockPath), 0o600); err != nil {
		t.Fatal(err)
	}
	file, unlock, err := openWorktreeLifecycleLockFile(root, lockPath)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("FIFO lifecycle lock error = %v, want not-a-regular-file", err)
	}
}

func TestLifecycleFaultSeamsLockErrors(t *testing.T) {
	repo := initTestRepo(t)
	sentinel := errors.New("lifecycle fault")
	originalOpen := openLifecycleGitRoot
	openLifecycleGitRoot = func(string) (*os.Root, error) { return nil, sentinel }
	t.Cleanup(func() { openLifecycleGitRoot = originalOpen })
	if _, err := LockWorktreeLifecycle(repo, "open-error"); !errors.Is(err, sentinel) {
		t.Fatalf("open root error = %v", err)
	}
	openLifecycleGitRoot = originalOpen

	originalLstat := lstatLifecyclePath
	lstatLifecyclePath = func(root *os.Root, path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, ".lock") {
			return nil, sentinel
		}
		return root.Lstat(path)
	}
	t.Cleanup(func() { lstatLifecyclePath = originalLstat })
	if _, err := LockWorktreeLifecycle(repo, "stat-error"); !errors.Is(err, sentinel) {
		t.Fatalf("lock stat error = %v", err)
	}
}

func TestLifecycleFaultSeamsLockDirectoryErrors(t *testing.T) {
	repo := initTestRepo(t)
	commonDir, err := WorktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("lifecycle directory fault")
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
	})
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return sentinel }
	if err := ensureRegularLifecycleLockDir(root); !errors.Is(err, sentinel) {
		t.Fatalf("mkdir error = %v", err)
	}

	calls := 0
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return nil, sentinel
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return nil }
	if err := ensureRegularLifecycleLockDir(root); !errors.Is(err, sentinel) {
		t.Fatalf("post-mkdir stat error = %v", err)
	}
}
