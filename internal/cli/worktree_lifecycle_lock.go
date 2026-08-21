package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

const worktreeLifecycleLockDir = "mivia-worktree-locks"

var openLifecycleGitRoot = os.OpenRoot
var lstatLifecyclePath = func(root *os.Root, path string) (os.FileInfo, error) { return root.Lstat(path) }
var mkdirLifecycleDir = func(root *os.Root, path string, mode os.FileMode) error { return root.Mkdir(path, mode) }

type worktreeLifecycleLock struct {
	file       *os.File
	gitRoot    *os.Root
	unlockFile func()
}

func (lock *worktreeLifecycleLock) Close() {
	lock.unlockFile()
	_ = lock.file.Close()
	_ = lock.gitRoot.Close()
}

func (lock *worktreeLifecycleLock) File() *os.File {
	return lock.file
}

// LockWorktreeLifecycle implements lock worktree lifecycle.
func LockWorktreeLifecycle(root, name string) (*worktreeLifecycleLock, error) {
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return nil, err
	}
	commonDir, err := worktreeGitCommonDir(root)
	if err != nil {
		return nil, err
	}
	gitRoot, err := openLifecycleGitRoot(commonDir)
	if err != nil {
		return nil, fmt.Errorf("open Git common directory: %w", err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = gitRoot.Close()
		}
	}()
	if err := ensureRegularLifecycleLockDir(gitRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(worktreeLifecycleLockDir, sanitized+".lock")
	if info, err := lstatLifecyclePath(gitRoot, path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, fmt.Errorf("worktree lifecycle lock is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect worktree lifecycle lock: %w", err)
	}
	file, unlockFile, err := openWorktreeLifecycleLockFile(gitRoot, path)
	if err != nil {
		return nil, err
	}
	closeRoot = false
	return &worktreeLifecycleLock{file: file, gitRoot: gitRoot, unlockFile: unlockFile}, nil
}

func ensureRegularLifecycleLockDir(root *os.Root) error {
	info, err := lstatLifecyclePath(root, worktreeLifecycleLockDir)
	if os.IsNotExist(err) {
		if mkdirErr := mkdirLifecycleDir(root, worktreeLifecycleLockDir, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create worktree lifecycle lock directory: %w", mkdirErr)
		}
		info, err = lstatLifecyclePath(root, worktreeLifecycleLockDir)
	}
	if err != nil {
		return fmt.Errorf("inspect worktree lifecycle lock directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("worktree lifecycle lock path is not a regular directory")
	}
	return nil
}
