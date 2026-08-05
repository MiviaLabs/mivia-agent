//go:build unix

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openWorktreeLifecycleLockFile(root *os.Root, path string) (*os.File, func(), error) {
	if filepath.Dir(path) != worktreeLifecycleLockDir {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: invalid path")
	}
	if _, err := root.Lstat("."); err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	rootFD, err := unix.Open(root.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	defer unix.Close(rootFD)
	dirFD, err := unix.Openat(rootFD, worktreeLifecycleLockDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock directory: %w", err)
	}
	defer unix.Close(dirFD)
	fileFD, err := unix.Openat(dirFD, filepath.Base(path), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("inspect worktree lifecycle lock: %w", err)
		}
		return nil, nil, fmt.Errorf("worktree lifecycle lock is not a regular file")
	}
	unlock, err := lockWorktreeMarkerFile(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, unlock, nil
}
