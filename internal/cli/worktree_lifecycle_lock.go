package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

const worktreeLifecycleLockDir = "mivia-worktree-locks"

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

func lockWorktreeLifecycle(root, name string) (*worktreeLifecycleLock, error) {
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return nil, err
	}
	commonDir, err := worktreeGitCommonDir(root)
	if err != nil {
		return nil, err
	}
	gitRoot, err := os.OpenRoot(commonDir)
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
	if info, err := gitRoot.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
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
	info, err := root.Lstat(worktreeLifecycleLockDir)
	if os.IsNotExist(err) {
		if err := root.Mkdir(worktreeLifecycleLockDir, 0o700); err != nil {
			return fmt.Errorf("create worktree lifecycle lock directory: %w", err)
		}
		info, err = root.Lstat(worktreeLifecycleLockDir)
	}
	if err != nil {
		return fmt.Errorf("inspect worktree lifecycle lock directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("worktree lifecycle lock path is not a regular directory")
	}
	return nil
}
